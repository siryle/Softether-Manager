package manager

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

func TestQuotaRestartKeepsBlockedUsage(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	path := "/api/v1/quotas/hub/" + url.PathEscape("Office / 上海")
	status, out, _ := call(t, a, "PUT", path, M{"limit": 0.0005, "unit": "MB"}, token)
	if status != 200 || !boolean(out["blocked"]) {
		t.Fatalf("initial block: %d %v", status, out)
	}
	fake.mu.Lock()
	fake.send, fake.recv = 10, 20
	fake.mu.Unlock()
	for range 2 {
		a.quotaTick(context.Background())
	}
	status, out, _ = call(t, a, "GET", path, nil, token)
	if status != 200 || integer(out["used_bytes"]) != 1030 || !boolean(out["blocked"]) {
		t.Fatalf("restart cleared quota: %d %v", status, out)
	}
	fake.mu.Lock()
	online := fake.hubOnline
	fake.mu.Unlock()
	if online {
		t.Fatal("restart reopened blocked hub")
	}
	status, out, _ = call(t, a, "POST", path+"/reset", nil, token)
	if status != 200 || integer(out["used_bytes"]) != 0 || boolean(out["blocked"]) {
		t.Fatalf("explicit reset failed: %d %v", status, out)
	}
}

func TestServerTrafficMissingAndNewHubs(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	id := a.serverIDFromContext(context.Background())
	start := time.Now().UTC().Add(-3 * time.Minute)
	for _, row := range []struct {
		hub    string
		offset int
		sent   int64
	}{
		{"A", 0, 1000}, {"B", 0, 5000}, {"A", 1, 1010}, {"A", 2, 1020}, {"B", 2, 5020}, {"C", 2, 9999999},
	} {
		stamp := start.Add(time.Duration(row.offset) * time.Minute).Format(time.RFC3339)
		execute(a.db, `INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,SampledDate) VALUES(?,?,?,?,?)`, id, row.hub, row.sent, 0, stamp)
	}
	status, out, _ := call(t, a, "GET", "/api/v1/server/traffic", nil, token)
	if status != 200 || integer(out["total_send"]) != 40 {
		t.Fatalf("false traffic spike: %d %v", status, out)
	}
	points := list(out["points"])
	if len(points) != 2 || integer(points[0]["send"]) != 10 || integer(points[1]["send"]) != 30 {
		t.Fatalf("wrong points: %v", points)
	}
}

func TestSessionRetentionIndependentOfTraffic(t *testing.T) {
	a := testApp(t)
	a.set("sample_retention_days", 7)
	a.set("session_history_retention_days", 30)
	session := execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,StartedDate,LastSeenDate) VALUES(1,'VPN','Alice','active',?,?)`, now(), now())
	for _, days := range []int{20, 31} {
		execute(a.db, `INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,SampledDate) VALUES(?,?)`, session, time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339))
	}
	a.prune()
	if count := integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionTrafficSample WHERE VpnSessionSampleID=?`, session)["n"]); count != 1 {
		t.Fatalf("kept %d points, want the 20-day point only", count)
	}
}

func TestSessionLogoutRevokesOnlyThatLogin(t *testing.T) {
	a := testApp(t)
	first := admin(t, a)
	status, out, rec := call(t, a, "POST", "/api/v1/auth/login", M{"username": "admin", "password": "correct horse battery"}, "")
	if status != 200 {
		t.Fatal(out)
	}
	second := rec.Result().Cookies()[0].Value
	if first == second {
		t.Fatal("logins must have distinct session IDs")
	}
	if _, exists := out["token"]; exists {
		t.Fatal("browser response exposes HttpOnly token")
	}
	status, _, _ = call(t, a, "POST", "/api/v1/auth/logout", nil, first)
	if status != 200 {
		t.Fatal(status)
	}
	for _, entry := range []struct {
		token  string
		status int
	}{{first, 401}, {second, 200}} {
		status, _, _ = call(t, a, "GET", "/api/v1/auth/me", nil, entry.token)
		if status != entry.status {
			t.Fatalf("logout: got %d want %d", status, entry.status)
		}
	}
	reopened, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, _, _ = call(t, reopened, "GET", "/api/v1/auth/me", nil, first)
	if status != 401 {
		t.Fatal("revocation lost on restart")
	}
	a.SetPassword("admin", "changed by CLI")
	status, _, _ = call(t, a, "GET", "/api/v1/auth/me", nil, second)
	if status != 401 {
		t.Fatal("CLI password reset retained old login")
	}
}

func TestLegacyUserSessionMigration(t *testing.T) {
	a := testApp(t)
	a.SetPassword("legacy", "old password")
	id := integer(one(a.db, `SELECT UserID FROM PanelUser WHERE Username='legacy'`)["UserID"])
	legacyToken := a.token(id, "legacy", 0)
	execute(a.db, `ALTER TABLE PanelUser DROP COLUMN SessionVersion`)
	reopened, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, _, _ := call(t, reopened, "GET", "/api/v1/auth/me", nil, legacyToken)
	if status != 401 {
		t.Fatal("legacy session was accepted after migration")
	}
	status, _, rec := call(t, reopened, "POST", "/api/v1/auth/login", M{"username": "legacy", "password": "old password"}, "")
	if status != 200 {
		t.Fatal("migration lost the user's password")
	}
	status, _, _ = call(t, reopened, "PUT", "/api/v1/auth/password", M{"current_password": "old password", "new_password": "new password"}, rec.Result().Cookies()[0].Value)
	if status != 200 {
		t.Fatal("migrated user cannot change password")
	}
}

func TestLoginLimiterAndOriginChecks(t *testing.T) {
	a := testApp(t)
	for i := range 9 {
		status, _, rec := call(t, a, "POST", "/api/v1/auth/login", M{"username": "missing", "password": "wrong"}, "")
		expected := 401
		if i == 8 {
			expected = 429
		}
		if status != expected {
			t.Fatalf("attempt %d: %d", i, status)
		}
		if i == 8 && rec.Header().Get("Retry-After") == "" {
			t.Fatal("missing retry delay")
		}
	}
	a.logins.mu.Lock()
	entry := a.logins.attempts["203.0.113.10"]
	entry.expires = time.Now().Add(-time.Second)
	a.logins.attempts["203.0.113.10"] = entry
	a.logins.mu.Unlock()
	status, _, _ := call(t, a, "POST", "/api/v1/auth/login", M{"username": "missing", "password": "wrong"}, "")
	if status != 401 {
		t.Fatal("expired rate limit did not clear")
	}
	var limiter loginLimiter
	var accepted atomic.Int32
	var wg sync.WaitGroup
	at := time.Now()
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.take("same", at) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 8 {
		t.Fatalf("concurrent limit: %d", accepted.Load())
	}
	for _, origin := range []string{"https://evil.example", "null", "http://panel.example.evil"} {
		req := httptest.NewRequest("POST", "http://panel.example/api/v1/auth/logout", nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("allowed origin %s: %d", origin, rec.Code)
		}
	}
	a.set("domain", "panel.example")
	a.set("domain_only", true)
	req := httptest.NewRequest("GET", "http://evil.example/api/v1/system/health", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal("local reverse proxy bypassed domain restriction")
	}
	req.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal("local healthcheck blocked")
	}
}

func TestCredentialEncryptionMigrationAndPermissions(t *testing.T) {
	a := testApp(t)
	execute(a.db, `INSERT INTO VpnUserCredential(ServerID,HubName,UserName,PasswordHash,UpdatedDate) VALUES(1,'VPN','Legacy','old-vpn-hash',?)`, now())
	if runtime.GOOS != "windows" {
		check(os.Chmod(a.cfg.Database, 0644))
	}
	reopened, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.storedCredential(1, "VPN", "Legacy"); got != "old-vpn-hash" {
		t.Fatal("migration lost credential")
	}
	reopened.saveCredential(1, "VPN", "New", "new-vpn-hash")
	for _, row := range query(a.db, `SELECT PasswordHash FROM VpnUserCredential`) {
		if !strings.HasPrefix(str(row["PasswordHash"]), "enc:") {
			t.Fatal("plaintext credential remains")
		}
	}
	if reopened.storedCredential(1, "VPN", "New") != "new-vpn-hash" {
		t.Fatal("new encrypted credential unusable")
	}
	if runtime.GOOS != "windows" {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			info, err := os.Stat(a.cfg.Database + suffix)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("%s mode: %o", suffix, info.Mode().Perm())
			}
		}
	}
}

func TestCertificatePinBlocksCredentialsBeforeHTTP(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": M{"ServerVersionString_str": "test"}})
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, portText, _ := strings.Cut(u.Host, ":")
	port := int(integer(portText))
	a.saveConnection(host, port, ptr("do-not-send-to-impostor"), "header")
	c := a.client()
	if _, err := c.Call(context.Background(), "GetServerInfo", nil); err != nil {
		t.Fatal(err)
	}
	original := certFingerprint(server.Certificate().Raw)
	if got := str(one(a.db, `SELECT Fingerprint FROM RPCTrust WHERE Host=? AND Port=?`, host, port)["Fingerprint"]); got != original {
		t.Fatal("first-use pin not saved")
	}
	// Simulate an independently issued replacement certificate.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	check(err)
	replacement := httptest.NewUnstartedServer(handler)
	replacement.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{raw}, PrivateKey: key}}}
	replacement.StartTLS()
	defer replacement.Close()
	c.Close()
	c.url = replacement.URL + "/api/"
	before := calls.Load()
	if _, err := c.Call(context.Background(), "GetServerInfo", nil); err == nil || !strings.Contains(err.Error(), "certificate changed") {
		t.Fatalf("accepted replacement: %v", err)
	}
	if calls.Load() != before {
		t.Fatal("administrator credential reached replacement server")
	}
	path := "/api/v1/servers/1/certificate"
	status, _, _ := call(t, a, "PUT", path, M{"fingerprint": strings.Repeat("0", 64)}, token)
	if status != 409 {
		t.Fatalf("accepted unobserved fingerprint: %d", status)
	}
	// Verify the explicit confirmation flow against the real, saved address.
	execute(a.db, `UPDATE RPCTrust SET Fingerprint=? WHERE Host=? AND Port=?`, strings.Repeat("0", 64), host, port)
	status, out, _ := call(t, a, "GET", path, nil, token)
	if status != 200 || str(out["fingerprint"]) != original {
		t.Fatalf("inspection: %d %v", status, out)
	}
	status, _, _ = call(t, a, "PUT", path, M{"fingerprint": original}, token)
	if status != 200 {
		t.Fatalf("confirmation: %d", status)
	}
	if _, err := a.client().Call(context.Background(), "GetServerInfo", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCertificateOrderWithExistingACMEAccount(t *testing.T) {
	var orders int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "bm9uY2U")
		switch r.URL.Path {
		case "/directory":
			writeJSON(w, 200, M{"newNonce": server.URL + "/nonce", "newAccount": server.URL + "/account", "newOrder": server.URL + "/order"})
		case "/nonce":
			w.WriteHeader(200)
		case "/account":
			w.Header().Set("Location", server.URL+"/account/1")
			writeJSON(w, 200, M{"status": "valid"})
		case "/order":
			orders++
			w.Header().Set("Location", server.URL+"/order/1")
			writeJSON(w, 201, M{"status": "pending", "identifiers": []M{{"type": "dns", "value": "vpn.example"}}, "authorizations": []string{}, "finalize": server.URL + "/finalize"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	for range 2 {
		client := &acme.Client{Key: key, DirectoryURL: server.URL + "/directory", HTTPClient: server.Client()}
		order, err := newCertificateOrder(context.Background(), client, &acme.Account{}, "vpn.example")
		if err != nil || order == nil {
			t.Fatalf("existing account blocked renewal: %v", err)
		}
	}
	if orders != 2 {
		t.Fatalf("created %d orders", orders)
	}
}

func TestCLIInitializeSoftEtherPassword(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(str(rejected), func(t *testing.T) {
			a := testApp(t)
			t.Setenv("SEM_DATA_DIR", a.cfg.DataDir)
			t.Setenv("SEM_APP_ROOT", a.cfg.Root)
			t.Setenv("SEM_DATABASE_URL", a.cfg.Database)
			t.Setenv("SEM_SECRET_KEY", a.cfg.SecretKey)
			t.Setenv("SEM_ENCRYPTION_PASSPHRASE", a.cfg.EncryptionKey)
			password := "generated initial password"
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("X-VPNADMIN-PASSWORD") != "" {
					t.Error("fresh connection must use the empty password")
				}
				var req M
				check(json.NewDecoder(r.Body).Decode(&req))
				if str(req["method"]) != "SetServerPassword" || str(object(req["params"])["PlainTextPassword_str"]) != password {
					t.Error("wrong bootstrap RPC")
				}
				if rejected {
					writeJSON(w, 200, M{"id": req["id"], "error": M{"code": 1, "message": "ERR_ACCESS_DENIED"}})
				} else {
					writeJSON(w, 200, M{"id": req["id"], "result": M{}})
				}
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			host, port, _ := strings.Cut(u.Host, ":")
			input, err := os.CreateTemp(t.TempDir(), "stdin")
			check(err)
			defer input.Close()
			_, err = input.WriteString(password)
			check(err)
			_, err = input.Seek(0, 0)
			check(err)
			saved := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = saved }()
			var out, stderr bytes.Buffer
			code := Run([]string{"connect", "--host", host, "--port", port, "--password-stdin", "--initialize-password"}, &out, &stderr)
			if (code != 0) != rejected || calls.Load() != 1 {
				t.Fatalf("bootstrap: exit=%d calls=%d", code, calls.Load())
			}
			if strings.Contains(out.String()+stderr.String(), password) {
				t.Fatal("password printed by CLI")
			}
			if rejected {
				if boolean(a.connection()["configured"]) {
					t.Fatal("failed bootstrap saved a connection")
				}
			} else {
				row := a.serverRow(a.serverIDFromContext(context.Background()))
				got, err := a.decrypt(str(row["EncryptedPassword"]))
				if err != nil || got != password {
					t.Fatal("initial password not saved correctly")
				}
			}
		})
	}
}

func TestCLIPasswordStdinPreservesBytes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SEM_DATA_DIR", dir)
	t.Setenv("SEM_APP_ROOT", dir)
	t.Setenv("SEM_DATABASE_URL", filepath.Join(dir, "manager.db"))
	input, err := os.CreateTemp(dir, "password-input")
	check(err)
	defer input.Close()
	password := "  spaces, quotes \" and 密码  "
	_, err = input.WriteString(password)
	check(err)
	_, err = input.Seek(0, 0)
	check(err)
	saved := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = saved }()
	var out, stderr bytes.Buffer
	if code := Run([]string{"password", "--username", "operator", "--password-stdin"}, &out, &stderr); code != 0 {
		t.Fatalf("CLI: %d %s", code, stderr.String())
	}
	db, err := openDB(filepath.Join(dir, "manager.db"))
	check(err)
	defer db.Close()
	if !VerifyPassword(password, str(one(db, `SELECT PasswordHash FROM PanelUser WHERE Username='operator'`)["PasswordHash"])) {
		t.Fatal("stdin password was trimmed or changed")
	}
}
