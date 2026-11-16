package manager

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUserPasswordRotationAndFreshCredential(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	hub := url.PathEscape("Office / 上海")
	path := "/api/v1/hubs/" + hub + "/users/Alice"
	status, user, _ := call(t, a, "GET", path, nil, token)
	if status != 200 || str(user["HashedKey_bin"]) == "" {
		t.Fatal(status, user)
	}
	user["Auth_Password_str"] = "replacement password"
	status, out, _ := call(t, a, "PUT", path, user, token)
	if status != 200 {
		t.Fatal(status, out)
	}
	fake.mu.Lock()
	got := fake.userHash
	// A password changed by another management client must supersede the panel cache.
	fake.userHash = hashedPassword("external password", "Alice")
	fake.mu.Unlock()
	if got != hashedPassword("replacement password", "Alice") {
		t.Fatal("the VPN server kept the old password")
	}
	status, out, _ = call(t, a, "POST", path+"/vpn-file", M{"host": "vpn.example", "embed_password": true}, token)
	if status != 200 || !strings.Contains(str(out["content"]), hashedPassword("external password", "Alice")) {
		t.Fatal("export used a stale credential", status, out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, req := range fake.calls {
		if req["method"] == "GetConfig" {
			t.Fatal("a user credential must not require the entire server configuration")
		}
	}
}

func TestRadiusPartialUpdatePreservesSecret(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	path := "/api/v1/hubs/" + url.PathEscape("Office / 上海") + "/radius"
	status, out, _ := call(t, a, "PUT", path, M{"RadiusServerName_str": "radius.example", "RadiusPort_u32": 1812}, token)
	if status != 200 {
		t.Fatal(status, out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.radius["RadiusSecret_str"] != "existing-radius-secret" {
		t.Fatal("partial update erased the RADIUS shared secret")
	}
}

func TestTrafficSamplerCannotOverwriteQuotaReading(t *testing.T) {
	a := testApp(t)
	fake := attachSoftEther(t, a)
	a.saveQuota(1, "hub", "Office / 上海", "", 100000, "total", true)
	a.observeQuotaLocked(1, "hub", "Office / 上海", "", 120, 0)
	fake.mu.Lock()
	fake.send, fake.recv = 110, 0
	fake.mu.Unlock()
	a.sampleTraffic(context.Background())
	if got := integer(quotaPublic(a.quotaRow(1, "hub", "Office / 上海", ""))["used_bytes"]); got != 120 {
		t.Fatalf("a stale traffic reading changed quota usage to %d", got)
	}
	fake.mu.Lock()
	fake.send = 130
	fake.mu.Unlock()
	a.quotaTick(context.Background())
	if got := integer(quotaPublic(a.quotaRow(1, "hub", "Office / 上海", ""))["used_bytes"]); got != 130 {
		t.Fatalf("quota usage doubled to %d", got)
	}
}

func TestQuotaBlockRetryKeepsOriginalRestoreState(t *testing.T) {
	a := testApp(t)
	fake := attachSoftEther(t, a)
	a.saveQuota(1, "hub", "Office / 上海", "", 100, "total", true)
	execute(a.db, `CREATE TRIGGER reject_block BEFORE UPDATE OF EnforcedDate ON TrafficQuota WHEN NEW.EnforcedDate IS NOT NULL BEGIN SELECT RAISE(ABORT,'simulated full disk'); END`)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the simulated final write did not fail")
			}
		}()
		a.blockQuota(context.Background(), a.quotaRow(1, "hub", "Office / 上海", ""))
	}()
	fake.mu.Lock()
	online := fake.hubOnline
	fake.mu.Unlock()
	if online || str(a.quotaRow(1, "hub", "Office / 上海", "")["RestoreState"]) == "" {
		t.Fatal("the original state was not durable before the remote block")
	}
	execute(a.db, `DROP TRIGGER reject_block`)
	a.blockQuota(context.Background(), a.quotaRow(1, "hub", "Office / 上海", ""))
	a.releaseQuota(context.Background(), a.quotaRow(1, "hub", "Office / 上海", ""))
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.hubOnline {
		t.Fatal("retry replaced the original online state with the blocked state")
	}
}

func TestQuotaCannotBeBypassedByReopeningHub(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	hub := url.PathEscape("Office / 上海")
	status, out, _ := call(t, a, "PUT", "/api/v1/quotas/hub/"+hub, M{"limit": 0.0005, "unit": "MB"}, token)
	if status != 200 || !boolean(out["blocked"]) {
		t.Fatal(status, out)
	}
	for _, path := range []string{"/api/v1/hubs/" + hub + "/online", "/api/v1/hubs/" + hub} {
		status, _, _ = call(t, a, "PUT", path, M{"online": true, "Online_bool": true}, token)
		if status != 409 {
			t.Fatalf("%s bypassed the quota: %d", path, status)
		}
	}
	fake.mu.Lock()
	fake.hubOnline = true // An external management client changed the remote state.
	fake.mu.Unlock()
	a.quotaTick(context.Background())
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.hubOnline {
		t.Fatal("the worker trusted its local blocked flag instead of the remote state")
	}
}

func TestQuotaServersDoNotBlockEachOtherAndStopAfterTransportFailure(t *testing.T) {
	a := testApp(t)
	fastID, _ := managedFake(t, a, "healthy")
	var attempts atomic.Int32
	bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(503)
	}))
	defer bad.Close()
	u, _ := url.Parse(bad.URL)
	badID := execute(a.db, `INSERT INTO Server(Name,Host,Port,EncryptedPassword,AuthType,CreatedDate,UpdatedDate) VALUES('unavailable',?,?,?,'header',?,?)`, u.Hostname(), integer(u.Port()), a.encrypt(""), now(), now())
	for _, name := range []string{"one", "two", "three"} {
		a.saveQuota(badID, "user", "VPN", name, 100, "total", true)
	}
	a.saveQuota(fastID, "user", "VPN", "Alice", 100, "total", true)
	lock := a.quotaLock(badID)
	lock.Lock()
	done := make(chan struct{})
	go func() { a.quotaTick(context.Background()); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for str(a.quotaRow(fastID, "user", "VPN", "Alice")["EnforcedDate"]) == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	healthyBlocked := str(a.quotaRow(fastID, "user", "VPN", "Alice")["EnforcedDate"]) != ""
	lock.Unlock()
	<-done
	if !healthyBlocked {
		t.Fatal("a busy server held up another server's quota enforcement")
	}
	if attempts.Load() != 1 {
		t.Fatalf("retried a failed server for every quota: %d calls", attempts.Load())
	}
}

func TestCLIConnectionUpdateInvalidatesLiveClient(t *testing.T) {
	a := testApp(t)
	attachSoftEther(t, a)
	before := a.client()
	cli, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cli.saveConnection("replacement.example", 443, ptr("new password"), "basic")
	after := a.client()
	if after == before || after.url != "https://replacement.example:443/api/" || after.password != "new password" || after.auth != "basic" {
		t.Fatal("the live panel retained the CLI's old connection")
	}
}

func TestAccessListsRejectStaleWrites(t *testing.T) {
	for _, kind := range []struct{ path, key string }{{"access", "AccessList"}, {"ac-list", "ACList"}} {
		t.Run(kind.path, func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			attachSoftEther(t, a)
			path := "/api/v1/hubs/" + url.PathEscape("Office / 上海") + "/" + kind.path
			_, first, _ := call(t, a, "GET", path, nil, token)
			body := M{kind.key: []M{{"Id_u32": 1, "Priority_u32": 100}}, "expected_version": first["version"]}
			status, out, _ := call(t, a, "PUT", path, body, token)
			if status != 200 {
				t.Fatal(status, out)
			}
			body[kind.key] = []M{}
			status, _, _ = call(t, a, "PUT", path, body, token)
			if status != 409 {
				t.Fatalf("stale list overwrote a newer rule: %d", status)
			}
			delete(body, "expected_version")
			status, _, _ = call(t, a, "PUT", path, body, token)
			if status != 428 {
				t.Fatalf("unchecked list replacement accepted: %d", status)
			}
			if kind.path == "access" {
				status, _, _ = call(t, a, "DELETE", path+"/1", M{"expected_version": first["version"]}, token)
				if status != 409 {
					t.Fatal("stale rule deletion accepted")
				}
			}
		})
	}
}

func TestCertificateBundleKeepsKeyChainAndMetadataTogether(t *testing.T) {
	a := testApp(t)
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.TLS.Certificates[0]
	domain := server.Certificate().DNSNames[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	check(err)
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	dir := a.tls.certDir(domain)
	check(saveCertificate(dir, chain, private, M{"staging": false, "issued_at": "original"}))
	before, err := os.ReadFile(filepath.Join(dir, "bundle.pem"))
	check(err)
	if err = saveCertificate(dir, chain, []byte("invalid private key"), M{"staging": true}); err == nil {
		t.Fatal("invalid replacement certificate was committed")
	}
	after, err := os.ReadFile(filepath.Join(dir, "bundle.pem"))
	check(err)
	if !bytes.Equal(before, after) {
		t.Fatal("failed replacement destroyed the working certificate")
	}
	info, loaded := a.tls.certificate(domain)
	if loaded == nil || boolean(info["stale"]) || info["issued_at"] != "original" {
		t.Fatal("certificate metadata was not loaded with its key and chain", info)
	}
}
