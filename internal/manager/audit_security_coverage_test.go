package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloudflareDNSClientAndBodyClosure(t *testing.T) {
	origBase := cfAPIBase
	defer func() { cfAPIBase = origBase }()

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer cf-secret-token" {
			t.Errorf("unexpected auth header: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "name=sub.example.com"):
			writeJSON(w, 200, M{"success": true, "result": []any{}})
		case r.Method == "GET" && strings.Contains(r.URL.RawQuery, "name=example.com"):
			writeJSON(w, 200, M{"success": true, "result": []M{{"id": "zone-123", "name": "example.com"}}})
		case r.Method == "POST" && r.URL.Path == "/zones/zone-123/dns_records":
			writeJSON(w, 200, M{"success": true, "result": M{"id": "rec-456"}})
		case r.Method == "DELETE" && r.URL.Path == "/zones/zone-123/dns_records/rec-456":
			writeJSON(w, 200, M{"success": true})
		default:
			t.Fatalf("unexpected Cloudflare request: %s %s", r.Method, r.URL.RequestURI())
		}
	}))
	defer srv.Close()
	cfAPIBase = srv.URL

	zoneID, err := cfFindZone(t.Context(), "cf-secret-token", "sub.example.com")
	if err != nil || zoneID != "zone-123" {
		t.Fatalf("cfFindZone = (%q, %v)", zoneID, err)
	}
	recID, err := cfCreateTXT(t.Context(), "cf-secret-token", zoneID, "_acme-challenge.sub.example.com", "val123")
	if err != nil || recID != "rec-456" {
		t.Fatalf("cfCreateTXT = (%q, %v)", recID, err)
	}
	if err := cfDeleteTXT(t.Context(), "cf-secret-token", zoneID, recID); err != nil {
		t.Fatalf("cfDeleteTXT error: %v", err)
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 requests (sub miss, apex hit, create, delete), got %v", seen)
	}
}

func TestCloudflareTokenEncryptedRedactedAndHookValidated(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// 1. Shell command injection in cert_renew_hook must be rejected.
	status, _, _ := call(t, a, "PUT", "/api/v1/system/domain", M{
		"cert_renew_hook": "ssl/hook.sh; id",
	}, token)
	if status != 422 {
		t.Fatalf("expected 422 for shell injection hook, got %d", status)
	}

	status, _, _ = call(t, a, "PUT", "/api/v1/system/domain", M{
		"cert_renew_hook": "/etc/passwd",
	}, token)
	if status != 422 {
		t.Fatalf("expected 422 for hook outside ssl/, got %d", status)
	}

	// 2. Saving cf_dns_token encrypts at rest and redacts on GET /system/domain and GET /system/settings.
	status, out, _ := call(t, a, "PUT", "/api/v1/system/domain", M{
		"domain":              "panel.example.com",
		"acme_challenge_type": "dns-01",
		"cf_dns_token":        "super-secret-cf-token",
		"cert_renew_hook":     "ssl/hook.sh",
	}, token)
	if status != 200 {
		t.Fatalf("PUT /system/domain: %d %v", status, out)
	}
	if str(out["cf_dns_token"]) != "" || !boolean(out["has_cf_dns_token"]) {
		t.Fatalf("expected cf_dns_token redacted and has_cf_dns_token=true, got %v", out)
	}

	rawStored := str(a.setting("cf_dns_token"))
	if rawStored == "" || strings.Contains(rawStored, "super-secret-cf-token") {
		t.Fatalf("cf_dns_token stored insecurely in DB: %q", rawStored)
	}
	if decrypted := a.secretSetting("cf_dns_token"); decrypted != "super-secret-cf-token" {
		t.Fatalf("secretSetting decrypted = %q", decrypted)
	}

	// Verify GET /system/settings also redacts cf_dns_token.
	status, sysOut, _ := call(t, a, "GET", "/api/v1/system/settings", nil, token)
	if status != 200 || str(sysOut["cf_dns_token"]) != "" || !boolean(sysOut["has_cf_dns_token"]) {
		t.Fatalf("GET /system/settings leaked cf_dns_token: %v", sysOut)
	}

	// Updating domain settings without sending cf_dns_token preserves existing secret.
	status, out, _ = call(t, a, "PUT", "/api/v1/system/domain", M{
		"acme_staging": true,
	}, token)
	if status != 200 || a.secretSetting("cf_dns_token") != "super-secret-cf-token" || !boolean(out["has_cf_dns_token"]) {
		t.Fatalf("omitted cf_dns_token cleared saved token: %v", out)
	}
}

func TestPublicHostScopedPerServerAndDecoupledFromDomain(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// Create a second server cleanly.
	status, s2, _ := call(t, a, "POST", "/api/v1/servers", M{
		"name":      "Node 2",
		"host":      "10.0.0.2",
		"port":      5555,
		"password":  "secret2",
		"skip_test": true,
	}, token)
	if status != 200 {
		t.Fatalf("POST /servers: %d %v", status, s2)
	}
	id2 := integer(s2["id"])

	// Set public_host on server 1 vs server 2.
	status, _, _ = call(t, a, "PUT", "/api/v1/server/connection", M{"public_host": "vpn1.example.com", "public_port": 443}, token)
	if status != 200 {
		t.Fatalf("save public endpoint s1: %d", status)
	}
	status, _, _ = callWithServer(t, a, "PUT", "/api/v1/server/connection", M{"public_host": "vpn2.example.com", "public_port": 1194}, token, id2)
	if status != 200 {
		t.Fatalf("save public endpoint s2: %d", status)
	}

	status, c1, _ := callWithServer(t, a, "GET", "/api/v1/server/connection", nil, token, 1)
	status2, c2, _ := callWithServer(t, a, "GET", "/api/v1/server/connection", nil, token, id2)
	if status != 200 || status2 != 200 || str(c1["public_host"]) != "vpn1.example.com" || str(c2["public_host"]) != "vpn2.example.com" || integer(c2["public_port"]) != 1194 {
		t.Fatalf("public endpoints leaked across servers: c1=%v c2=%v", c1, c2)
	}
	if a.tls.effectiveDomain() != "" {
		t.Fatalf("vpn_public_host polluted TLS effectiveDomain: %q", a.tls.effectiveDomain())
	}
}

func callWithServer(t *testing.T, a *App, method, path string, body M, token string, serverID int64) (int, M, *httptest.ResponseRecorder) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = "localhost"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if serverID > 0 {
		req.Header.Set("X-Server-ID", str(serverID))
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var out M
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}

func TestQuotaTickDoesNotBlockWritesDuringSlowRPC(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	inRPC := make(chan struct{})
	releaseRPC := make(chan struct{})
	var once sync.Once
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		_ = json.NewDecoder(r.Body).Decode(&req)
		if str(req["method"]) == "GetUser" {
			once.Do(func() { close(inRPC) })
			<-releaseRPC
		}
		writeJSON(w, 200, M{
			"jsonrpc": "2.0",
			"id":      req["id"],
			"result": M{
				"Name_str":                "Alice",
				"Send.UnicastBytes_u64":   100,
				"Send.BroadcastBytes_u64": 0,
				"Recv.UnicastBytes_u64":   100,
				"Recv.BroadcastBytes_u64": 0,
				"UsePolicy_bool":          true,
				"policy:Access_bool":      true,
			},
		})
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")
	a.saveQuota(1, "user", "DEFAULT", "Alice", 1<<30, "total", true)

	doneTick := make(chan struct{})
	go func() {
		defer close(doneTick)
		a.quotaTick(t.Context())
	}()

	<-inRPC
	// While quotaTick is waiting inside GetUser RPC, a mutating request on server 1 must NOT deadlock/block on quotaLock.
	doneWrite := make(chan int, 1)
	go func() {
		status, _, _ := call(t, a, "PUT", "/api/v1/server/backup-settings", M{"enabled": true}, token)
		doneWrite <- status
	}()

	select {
	case st := <-doneWrite:
		if st != 200 {
			t.Fatalf("expected concurrent write 200 while quotaTick fetched telemetry, got %d", st)
		}
	case <-time.After(500 * time.Millisecond):
		close(releaseRPC)
		t.Fatal("mutating HTTP request was blocked by quotaTick holding quotaLock during RPC")
	}
	close(releaseRPC)
	<-doneTick
}

func TestPushWireGuardConfigDistinguishesDialErrorFromEOF(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	a.saveQuota(1, "hub", "DEFAULT", "", 1<<30, "total", true)
	// 1. Unreachable SoftEther port (dial error): must fail 502 and MUST NOT reset quota baselines or add DB peer.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()
	host, port, _ := strings.Cut(closedAddr, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")
	execute(a.db, `UPDATE TrafficQuota SET CycleStartDate='2026-01-01T00:00:00Z',BaseSendBytes=500,LastSendBytes=900 WHERE ServerID=1`)

	err = recoverAsError(func() error {
		a.pushWireGuardConfig(context.WithValue(t.Context(), serverIDKey, int64(1)), wgTestConfig5x)
		return nil
	})
	if err == nil {
		t.Fatal("expected error when SoftEther port is unreachable")
	}
	q := a.quotaRow(1, "hub", "DEFAULT", "")
	if integer(q["BaseSendBytes"]) != 500 || str(q["CycleStartDate"]) == "" {
		t.Fatalf("quota baseline was wiped despite unreachable SoftEther: %v", q)
	}

	// 2. Post-write EOF (SoftEther restarted right after accepting SetConfig): must succeed & reset baseline.
	eofSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		_ = json.NewDecoder(r.Body).Decode(&req)
		if str(req["method"]) == "SetConfig" {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("ResponseWriter does not support Hijack")
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()
			return
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": M{
			"FileName_str": "vpn_server.config",
			"FileData_bin": base64.StdEncoding.EncodeToString([]byte(wgTestConfig5x)),
		}})
	}))
	defer eofSrv.Close()
	u, _ := url.Parse(eofSrv.URL)
	h2, p2, _ := strings.Cut(u.Host, ":")
	a.saveConnection(h2, int(integer(p2)), ptr("vpn-secret"), "header")

	status, _, _ := call(t, a, "PUT", "/api/v1/server/wireguard", M{
		"confirm_restart": true,
		"wireguard_port":  51825,
	}, token)
	if status != 200 {
		t.Fatalf("expected 200 on SetConfig restart EOF, got %d", status)
	}
	q = a.quotaRow(1, "hub", "DEFAULT", "")
	if integer(q["BaseSendBytes"]) != 0 || str(q["CycleStartDate"]) != "" {
		t.Fatalf("quota baseline was not reset after SetConfig restart: %v", q)
	}

	// 3. CR/LF injection sanitization in WireGuard client config: no new INI directives may be injected.
	cfgText := buildWireGuardClientConfig("priv\nInjected=1", "10.0.0.2", "1.1.1.1\r\nPostUp = evil", "srvPub", "", "vpn.example.com\nEvil", 51820, "0.0.0.0/0", 25)
	if strings.Contains(cfgText, "\nPostUp") || strings.Contains(cfgText, "\nInjected") || strings.Contains(cfgText, "\nEvil") {
		t.Fatalf("newline injection survived in client config:\n%s", cfgText)
	}
}

func TestBatchLookupIPsPanicSafetyAndNegativeCache(t *testing.T) {
	a := testApp(t)
	origTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = origTransport }()

	var calls int
	http.DefaultTransport = auditTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: http.Header{}}, nil
	})

	// Negative cache prevents repeated blocking on identical failed IP in batchLookupIPs.
	_ = a.batchLookupIPs(t.Context(), []string{"198.51.100.200"})
	_ = a.batchLookupIPs(t.Context(), []string{"198.51.100.200"})
	if calls != 1 {
		t.Fatalf("expected negative cache to limit failed batch calls to 1, got %d", calls)
	}

	// Even if DB is closed mid-lookup, batchLookupIPs must NOT panic inside worker goroutines.
	_ = a.db.Close()
	out := a.batchLookupIPs(t.Context(), []string{"198.51.100.201"})
	if out == nil {
		t.Fatal("expected non-nil safe return map without panic")
	}
}

func TestResourceReadersAndUpdateHelpers(t *testing.T) {
	origProc := procRoot
	defer func() { procRoot = origProc }()

	fakeProc := t.TempDir()
	_ = os.WriteFile(filepath.Join(fakeProc, "stat"), []byte("cpu  100 10 50 800 20 0 20 0 0 0\ncpu0 50 5 25 400 10 0 10 0 0 0\n"), 0644)
	_ = os.WriteFile(filepath.Join(fakeProc, "net"), []byte(""), 0644) // unused placeholder; create net/dev properly
	_ = os.Remove(filepath.Join(fakeProc, "net"))
	_ = os.MkdirAll(filepath.Join(fakeProc, "net"), 0755)
	_ = os.WriteFile(filepath.Join(fakeProc, "net", "dev"), []byte("Inter-| Receive\n  lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\neth0: 2048 10 0 0 0 0 0 0 4096 20 0 0 0 0 0 0\n"), 0644)
	_ = os.WriteFile(filepath.Join(fakeProc, "meminfo"), []byte("MemTotal: 1048576 kB\nMemAvailable: 524288 kB\nMemFree: 262144 kB\nSwapTotal: 524288 kB\nSwapFree: 524288 kB\n"), 0644)
	_ = os.WriteFile(filepath.Join(fakeProc, "mounts"), []byte("tmpfs /tmp tmpfs rw 0 0\n"), 0644)
	_ = os.WriteFile(filepath.Join(fakeProc, "loadavg"), []byte("0.25 0.30 0.35 1/120 999\n"), 0644)
	procRoot = fakeProc

	cpu := readCPU()
	if len(cpu) != 2 || cpu["cpu"][0] != 100 {
		t.Fatalf("unexpected readCPU: %v", cpu)
	}
	netMap := readNetwork()
	if netMap["eth0"][0] != 2048 || netMap["eth0"][2] != 4096 {
		t.Fatalf("unexpected readNetwork: %v", netMap)
	}
	mem, swap := readMemory()
	if integer(mem["total_bytes"]) != 1048576*1024 || integer(swap["used_bytes"]) != 0 {
		t.Fatalf("unexpected readMemory: %v %v", mem, swap)
	}

	if !isNewer("v0.1.0", "v0.2.0") || isNewer("v0.2.0", "v0.1.0") || isNewer("0.0.0-dev", "v1.0.0") {
		t.Fatal("isNewer logic failed")
	}
	a := testApp(t)
	a.writeUpdateRun(M{"stage": "idle", "target_version": "v1.2.3"})
	if str(a.readUpdateRun()["target_version"]) != "v1.2.3" {
		t.Fatal("readUpdateRun mismatch")
	}
}
