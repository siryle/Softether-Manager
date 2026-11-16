package manager

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Root: dir, DataDir: dir, Database: filepath.Join(dir, "manager.db"), BindHost: "127.0.0.1", BindPort: 18000, SessionMinutes: 60, Version: "1.0.13", Repo: "example/test", SEHost: "127.0.0.1", SEPort: 5555, SEAuth: "header", SecretKey: "fixture-session-key", EncryptionKey: "fixture-encryption-key"}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	a.set("update_check_enabled", false)
	return a
}
func fixture(t *testing.T, name string) M {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out M
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func call(t *testing.T, a *App, method, path string, body any, token string) (int, M, *httptest.ResponseRecorder) {
	t.Helper()
	var raw io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		raw = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, raw)
	req.RemoteAddr = "203.0.113.10:5555"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var out M
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}
func admin(t *testing.T, a *App) string {
	t.Helper()
	status, b, rec := call(t, a, "POST", "/api/v1/auth/setup", M{"username": "admin", "password": "correct horse battery"}, "")
	if status != 200 {
		t.Fatalf("setup: %d %v", status, b)
	}
	return rec.Result().Cookies()[0].Value
}
func TestPythonSecurityCompatibility(t *testing.T) {
	a := testApp(t)
	f := fixture(t, "security-python.json")
	if !VerifyPassword(str(f["password"]), str(f["password_hash"])) || VerifyPassword("wrong", str(f["password_hash"])) {
		t.Fatal("legacy scrypt hash")
	}
	plain, err := a.decrypt(str(f["fernet"]))
	if err != nil || plain != str(f["plaintext"]) {
		t.Fatalf("legacy Fernet: %q %v", plain, err)
	}
	encrypted := a.encrypt(plain)
	roundtrip, err := a.decrypt(encrypted)
	if err != nil || roundtrip != plain {
		t.Fatal("Fernet roundtrip")
	}
	raw, _ := base64.URLEncoding.DecodeString(encrypted)
	raw[len(raw)-1] ^= 1
	if _, err = a.decrypt(base64.URLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	claims := a.claims(str(f["token"]))
	if claims == nil || integer(claims["uid"]) != 7 {
		t.Fatal("legacy signed token rejected")
	}
	if a.claims(str(f["token"])+"x") != nil {
		t.Fatal("forged token accepted")
	}
}
func TestSecretFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.key")
	value, err := secret(path, "")
	if err != nil || value == "" {
		t.Fatalf("generate secret: %v", err)
	}
	saved, err := secret(path, "")
	if err != nil || saved != value {
		t.Fatal("secret changed on restart")
	}
	info, err := os.Stat(path)
	if runtime.GOOS != "windows" && (err != nil || info.Mode().Perm() != 0600) {
		t.Fatal("secret must be owner-only")
	}
	check(os.WriteFile(path, nil, 0600))
	if _, err = secret(path, ""); err == nil {
		t.Fatal("empty secret must fail without replacing the key")
	}
	if value, err = secret(path, "override"); err != nil || value != "override" {
		t.Fatal("explicit secret override rejected")
	}
}

func TestInstalledRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.env")
	t.Setenv("SEM_CLI_ENV_PATH", path)
	t.Setenv("SEM_RELEASE_REPO", "fallback/panel")
	if installedRepository() != "fallback/panel" {
		t.Fatal("repository environment fallback")
	}
	check(os.WriteFile(path, []byte("# installer metadata\nRELEASE_REPO='installed/fork'\n"), 0600))
	if installedRepository() != "installed/fork" {
		t.Fatal("installed fork must keep its own release feed")
	}
}

func TestSystemServiceContract(t *testing.T) {
	a := testApp(t)
	status, out, _ := call(t, a, "GET", "/api/v1/system/info", nil, admin(t, a))
	service := object(out["service"])
	if status != 200 {
		t.Fatalf("system info: %d %v", status, out)
	}
	for _, key := range []string{"unit", "active", "sub", "since"} {
		if _, ok := service[key].(string); !ok {
			t.Errorf("service.%s must be a string", key)
		}
	}
	if _, ok := service["managed"].(bool); !ok {
		t.Fatal("service.managed must be a boolean")
	}
}

func TestExistingCertificateAndChallenge(t *testing.T) {
	a := testApp(t)
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.TLS.Certificates[0]
	domain := server.Certificate().DNSNames[0]
	dir := a.tls.certDir(domain)
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	check(err)
	check(atomicWrite(filepath.Join(dir, "privkey.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	check(atomicWrite(filepath.Join(dir, "fullchain.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600))
	check(atomicWrite(filepath.Join(dir, "meta.json"), []byte(`{"staging":false,"issued_at":"2026-01-01T00:00:00Z"}`), 0600))
	a.set("domain", domain)
	info, loaded := a.tls.certificate(domain)
	if loaded == nil || !boolean(info["matches_domain"]) || boolean(info["stale"]) || str(info["issued_at"]) != "2026-01-01T00:00:00Z" {
		t.Fatalf("existing certificate rejected: %v", info)
	}
	signer, err := loadOrCreateKey(filepath.Join(dir, "privkey.pem"))
	if err != nil || signer == nil {
		t.Fatalf("existing PKCS8 key rejected: %v", err)
	}
	a.set("web_path", "hidden")
	a.set("domain_only", true)
	a.tls.tokens["test-token"] = "test-response"
	req := httptest.NewRequest("GET", "http://unrecognized.example/.well-known/acme-challenge/test-token", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.String() != "test-response" {
		t.Fatal("ACME validation must work outside the secret prefix and domain restriction")
	}
}
func TestPythonVPNCompatibility(t *testing.T) {
	f := fixture(t, "vpn-python.json")
	if hex.EncodeToString(sha0([]byte("abc"))) != str(f["sha0"]) {
		t.Fatal("SHA-0 vector")
	}
	if hashedPassword("test密码", "Alice") != str(f["hashed_password"]) {
		t.Fatal("SoftEther password hash")
	}
	if proxyPassword("proxy pass") != str(f["proxy_password"]) {
		t.Fatal("proxy RC4 compatibility")
	}
	doc := buildVPN("vpn.example.com", 443, "Office / 上海", "Alice", 1, "", "test密码", "", M{"ProxyPassword": "proxy pass"})
	if doc != str(f["vpn_file"]) {
		t.Fatalf("VPN output differs from Python:\n%q\nexpected:\n%q", doc, f["vpn_file"])
	}
	if cfgEscape("a b$\n") != "a$20b$24$0A" {
		t.Fatal("config escaping")
	}
	if safeFilename("../../Alice") != "Alice.vpn" {
		t.Fatal("filename safety")
	}
}
func TestEntireRESTContractRegistered(t *testing.T) {
	a := testApp(t)
	raw, err := os.ReadFile("testdata/api-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes []struct{ Method, Path string }
	check(json.Unmarshal(raw, &routes))
	for _, route := range routes {
		path := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(route.Path, "example")
		req := httptest.NewRequest(route.Method, "/api/v1"+path, nil)
		_, pattern := a.mux.Handler(req)
		if pattern == "/" || pattern == "" {
			t.Errorf("missing legacy route: %s %s (%s)", route.Method, route.Path, pattern)
		}
	}
	if len(routes) != 183 {
		t.Fatalf("unexpected contract count %d", len(routes))
	}
}
func TestAuthAndPersistence(t *testing.T) {
	a := testApp(t)
	status, body, _ := call(t, a, "GET", "/api/v1/auth/state", nil, "")
	if status != 200 || !boolean(body["setup_required"]) {
		t.Fatal("initial setup state")
	}
	token := admin(t, a)
	status, _, rec := call(t, a, "POST", "/api/v1/auth/login", M{"username": "admin", "password": "correct horse battery"}, "")
	if status != 200 {
		t.Fatal("login")
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie flags")
	}
	request := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	request.AddCookie(cookies[0])
	rr := httptest.NewRecorder()
	a.Handler().ServeHTTP(rr, request)
	if rr.Code != 200 {
		t.Fatal("cookie authentication")
	}
	status, _, _ = call(t, a, "POST", "/api/v1/auth/setup", M{"username": "attacker", "password": "test"}, "")
	if status != 409 {
		t.Fatal("setup reopened")
	}
	status, _, _ = call(t, a, "GET", "/api/v1/system/settings", nil, "")
	if status != 401 {
		t.Fatal("unauthenticated settings")
	}
	status, _, _ = call(t, a, "PUT", "/api/v1/auth/password", M{"current_password": "wrong", "new_password": "next"}, token)
	if status != 403 {
		t.Fatal("password change without old password")
	}
	status, _, _ = call(t, a, "PUT", "/api/v1/auth/password", M{"current_password": "correct horse battery", "new_password": "next"}, token)
	if status != 200 {
		t.Fatal("password update")
	}
	status, body, rec = call(t, a, "POST", "/api/v1/auth/login", M{"username": "admin", "password": "next"}, "")
	if status != 200 {
		t.Fatal("new password")
	}
	status, _, _ = call(t, a, "GET", "/api/v1/auth/me", nil, token)
	if status != 401 {
		t.Fatal("old session survived password change")
	}
	token = rec.Result().Cookies()[0].Value
	a.set("ui_live_seconds", 9)
	b, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if integer(b.setting("ui_live_seconds")) != 9 {
		t.Fatal("settings persistence")
	}
	status, _, _ = call(t, b, "GET", "/api/v1/auth/me", nil, token)
	if status != 200 {
		t.Fatal("session persistence")
	}
}
func TestSetupRace(t *testing.T) {
	a := testApp(t)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _, _ := call(t, a, "POST", "/api/v1/auth/setup", M{"username": fmt.Sprint("admin", i), "password": "safe"}, "")
			codes <- status
		}(i)
	}
	wg.Wait()
	close(codes)
	success, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("setup race: %d successes, %d conflicts", success, conflict)
	}
}
func TestMountHostGuardAndSettingsValidation(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	a.set("web_path", "secret")
	status, _, _ := call(t, a, "GET", "/api/v1/system/health", nil, "")
	if status != 404 {
		t.Fatal("prefix leaked")
	}
	status, _, _ = call(t, a, "GET", "/secret/api/v1/system/health", nil, "")
	if status != 200 {
		t.Fatal("mounted health")
	}
	a.set("domain", "panel.example.com")
	a.set("domain_only", true)
	status, _, _ = call(t, a, "GET", "/secret/api/v1/auth/me", nil, token)
	if status != 404 {
		t.Fatal("host guard bypass")
	}
	r := httptest.NewRequest("GET", "/secret/api/v1/auth/me", nil)
	r.Host = "panel.example.com:443"
	r.RemoteAddr = "203.0.113.1:55"
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal("domain access refused")
	}
	r.RemoteAddr = "127.0.0.1:55"
	r.Host = "127.0.0.1"
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatal("loopback maintenance refused")
	}
	a.set("domain_only", false)
	status, _, _ = call(t, a, "PUT", "/secret/api/v1/system/settings", M{"ui_live_seconds": 9, "sample_interval_minutes": 0}, token)
	if status != 422 || integer(a.setting("ui_live_seconds")) != 5 {
		t.Fatal("partial invalid settings save")
	}
	status, _, _ = call(t, a, "PUT", "/secret/api/v1/system/settings", M{"se_password": "override"}, token)
	if status != 422 {
		t.Fatal("connection changed through general settings")
	}
	status, _, _ = call(t, a, "PUT", "/secret/api/v1/system/domain", M{"domain_only": true}, token)
	if status != 422 {
		t.Fatal("domain-only lockout guard")
	}
}

type fakeSoftEther struct {
	mu                    sync.Mutex
	calls                 []M
	logSettings           M
	listeners             []M
	hubOnline, userAccess bool
	send, recv            int64
	userHash              string
	radius                M
	access, ac            []M
	failRestore           bool
}

func attachSoftEther(t *testing.T, a *App) *fakeSoftEther {
	t.Helper()
	fake := &fakeSoftEther{hubOnline: true, userAccess: true, send: 600, recv: 400, userHash: hashedPassword("secret", "Alice"), radius: M{"RadiusSecret_str": "existing-radius-secret"}, access: []M{}, ac: []M{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		if decoder.Decode(&req) != nil {
			t.Error("bad request")
		}
		if r.URL.Path != "/api/" || r.Header.Get("X-VPNADMIN-PASSWORD") != "vpn-secret" {
			t.Error("RPC endpoint/auth mismatch")
		}
		if _, ok := req["id"].(string); !ok {
			t.Error("SoftEther requires a string request ID")
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.calls = append(fake.calls, req)
		method := str(req["method"])
		params := object(req["params"])
		out := clone(params)
		switch method {
		case "EnumListener":
			out = M{"ListenerList": fake.listeners}
		case "EnableListener":
			for _, listener := range fake.listeners {
				if integer(listener["Ports_u32"]) == integer(params["Port_u32"]) {
					listener["Enables_bool"] = boolean(params["Enable_bool"])
				}
			}
		case "SetHubLog":
			fake.logSettings = clone(params)
		case "GetHubLog":
			out = clone(fake.logSettings)
		case "EnumHub":
			out = M{"HubList": []M{{"HubName_str": "Office / 上海", "NumUsers_u32": 1, "NumSessions_u32": 1}}}
		case "GetHub", "GetHubStatus":
			out = M{"Online_bool": fake.hubOnline, "Send.UnicastBytes_u64": fake.send, "Recv.UnicastBytes_u64": fake.recv, "NumUsers_u32": 1, "NumSessions_u32": 1}
		case "SetHubOnline":
			if boolean(params["Online_bool"]) && fake.failRestore {
				writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "error": M{"code": 1, "message": "ERR_BUSY"}})
				return
			}
			fake.hubOnline = boolean(params["Online_bool"])
		case "GetUser":
			out = M{"Name_str": params["Name_str"], "AuthType_u32": 1, "HashedKey_bin": fake.userHash, "UsePolicy_bool": true, "policy:Access_bool": fake.userAccess, "Send.UnicastBytes_u64": fake.send, "Recv.UnicastBytes_u64": fake.recv}
		case "SetUser":
			// SoftEther gives a supplied hash precedence over Auth_Password.
			if hash := str(params["HashedKey_bin"]); hash != "" {
				fake.userHash = hash
			} else if password, ok := params["Auth_Password_str"]; ok {
				fake.userHash = hashedPassword(str(password), str(params["Name_str"]))
			}
			if v, ok := params["policy:Access_bool"]; ok {
				fake.userAccess = boolean(v)
			}
		case "EnumUser":
			out = M{"UserList": []M{{"Name_str": "Alice", "Ex.Send.UnicastBytes_u64": fake.send, "Ex.Recv.UnicastBytes_u64": fake.recv, "NumLogin_u32": 1}}}
		case "EnumSession":
			out = M{"SessionList": []M{{"Name_str": "SID/ABC", "Username_str": "ALICE", "UniqueId_bin": "stable-key", "ClientIP_ip": "192.0.2.1", "CreatedTime_dt": now(), "PacketSize_u64": fake.send + fake.recv}}}
		case "GetSessionStatus":
			out = M{"Send.UnicastBytes_u64": fake.send, "Recv.UnicastBytes_u64": fake.recv}
		case "GetHubRadius":
			out = clone(fake.radius)
		case "SetHubRadius":
			fake.radius = clone(params)
			fake.radius["RadiusSecret_str"] = str(params["RadiusSecret_str"])
		case "EnumAccess":
			out = M{"AccessList": fake.access}
		case "AddAccess":
			for _, rule := range list(params["AccessListSingle"]) {
				rule["Id_u32"] = len(fake.access) + 1
				fake.access = append(fake.access, rule)
			}
		case "SetAccessList":
			fake.access = list(params["AccessList"])
		case "DeleteAccess":
			kept := []M{}
			for _, rule := range fake.access {
				if integer(rule["Id_u32"]) != integer(params["Id_u32"]) {
					kept = append(kept, rule)
				}
			}
			fake.access = kept
		case "GetAcList":
			out = M{"ACList": fake.ac}
		case "SetAcList":
			fake.ac = list(params["ACList"])
		case "ReadLogFile":
			out = M{"Buffer_bin": base64.StdEncoding.EncodeToString([]byte("test log\n"))}
		case "GetServerInfo":
			out = M{"ServerVersionString_str": "test", "ServerHostName_str": "vpnserver"}
		case "GetServerStatus":
			out = M{"NumSessionsTotal_u32": 1}
		case "GetConfig":
			out = M{"FileData_bin": base64.StdEncoding.EncodeToString([]byte("declare root\n{\n declare VirtualHUB\n {\n declare Office / 上海\n {\n declare SecurityAccountDatabase\n {\n declare UserList\n {\n declare Alice\n {\n byte AuthPassword " + hashedPassword("secret", "Alice") + "\n }\n }\n }\n }\n }\n}\n"))}
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": out})
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")
	return fake
}
func ptr(s string) *string { return &s }
func TestRPCGatewayAndEncodedPaths(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	hub := url.PathEscape("Office / 上海")
	status, out, _ := call(t, a, "GET", "/api/v1/hubs/"+hub, nil, token)
	if status != 200 || !boolean(out["Online_bool"]) {
		t.Fatalf("encoded hub %d %v", status, out)
	}
	status, out, _ = call(t, a, "GET", "/api/v1/hubs/"+hub+"/sessions/"+url.PathEscape("SID/ABC"), nil, token)
	if status != 200 {
		t.Fatalf("session path: %d %v", status, out)
	}
	status, out, _ = call(t, a, "GET", "/api/v1/server/logs/read?path=test.log&offset=128", nil, token)
	if status != 200 || str(out["text"]) != "test log\n" || integer(out["bytes"]) != 9 {
		t.Fatal("log decode")
	}
	status, out, _ = call(t, a, "GET", "/api/v1/hubs/"+hub+"/users/Alice/sessions", nil, token)
	if status != 200 || len(list(out["SessionList"])) != 1 {
		t.Fatal("case-insensitive user sessions")
	}
	status, out, _ = call(t, a, "GET", "/api/v1/hubs/"+hub+"/users/Alice/credential-state", nil, token)
	if status != 200 || !boolean(out["available"]) {
		t.Fatal("credential discovery")
	}
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/"+hub+"/users/Alice/vpn-file", M{"host": "vpn.example.com", "port": 443, "embed_password": true}, token)
	if status != 200 || !strings.Contains(str(out["content"]), hashedPassword("secret", "Alice")) {
		t.Fatal("cached VPN credential export")
	}
	status, _, _ = call(t, a, "POST", "/api/v1/rpc/call", M{"method": "ArbitraryUnknown"}, token)
	if status != 422 {
		t.Fatal("undocumented RPC accepted")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	found := false
	for _, entry := range fake.calls {
		if str(entry["method"]) == "GetHub" {
			found = str(object(entry["params"])["HubName_str"]) == "Office / 上海"
		}
	}
	if !found {
		t.Fatal("decoded path parameter lost")
	}
}
func TestQuotaBlockRestoreAndCounterReset(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	path := "/api/v1/quotas/hub/" + url.PathEscape("Office / 上海")
	status, out, _ := call(t, a, "PUT", path, M{"limit": 0.0005, "unit": "MB", "metric": "total", "enabled": true}, token)
	if status != 200 || !boolean(out["blocked"]) {
		t.Fatalf("quota block: %d %v", status, out)
	}
	fake.mu.Lock()
	if fake.hubOnline {
		t.Error("hub was not cut off")
	}
	fake.failRestore = true
	fake.mu.Unlock()
	status, out, _ = call(t, a, "PUT", path+"/enabled", M{"enabled": false}, token)
	if status != 200 || !boolean(out["blocked"]) {
		t.Fatal("failed restore must remain retryable")
	}
	fake.mu.Lock()
	fake.failRestore = false
	fake.mu.Unlock()
	a.quotaTick(context.Background())
	status, out, _ = call(t, a, "GET", path, nil, token)
	if status != 200 || boolean(out["blocked"]) {
		t.Fatal("restore retry")
	}
	status, out, _ = call(t, a, "POST", path+"/reset", nil, token)
	if status != 200 || integer(out["used_bytes"]) != 0 || integer(out["base_send_bytes"]) != 600 {
		t.Fatal("quota reset baseline")
	}
	fake.mu.Lock()
	fake.send, fake.recv = 10, 20
	fake.mu.Unlock()
	status, out, _ = call(t, a, "GET", path, nil, token)
	if status != 200 || integer(out["used_bytes"]) != 30 || integer(out["base_send_bytes"]) != 0 {
		t.Fatal("counter restart")
	}
	userPath := "/api/v1/quotas/user/" + url.PathEscape("Office / 上海") + "/Alice"
	status, out, _ = call(t, a, "PUT", userPath, M{"limit": 0.00001, "unit": "MB", "enabled": true}, token)
	if status != 200 || !boolean(out["blocked"]) {
		t.Fatal("user quota")
	}
	status, _, _ = call(t, a, "DELETE", userPath, nil, token)
	if status != 200 {
		t.Fatal("remove quota")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.userAccess {
		t.Fatal("user policy not restored")
	}
}
func TestTrafficAndSessionSampler(t *testing.T) {
	a := testApp(t)
	fake := attachSoftEther(t, a)
	ctx := context.Background()
	a.sampleTraffic(ctx)
	a.sampleSessions(ctx)
	if len(query(a.db, `SELECT * FROM HubTrafficSample`)) != 1 || len(query(a.db, `SELECT * FROM UserTrafficSample`)) != 1 {
		t.Fatal("traffic snapshots")
	}
	rows := query(a.db, `SELECT * FROM VpnSessionSample`)
	if len(rows) != 1 || integer(rows[0]["DownloadBytes"]) != 600 {
		t.Fatal("session history direction")
	}
	fake.mu.Lock()
	fake.send = 700
	fake.recv = 450
	fake.mu.Unlock()
	a.sampleTraffic(ctx)
	a.sampleSessions(ctx)
	rows = query(a.db, `SELECT * FROM VpnSessionTrafficSample ORDER BY VpnSessionTrafficSampleID`)
	out := usage(rows, 1, "UploadBytes", "DownloadBytes", false)
	if integer(out["total_recv"]) != 100 || integer(out["total_send"]) != 50 {
		t.Fatal("session traffic deltas")
	}
	out = usage([]M{{"SendBytes": 100, "RecvBytes": 90}, {"SendBytes": 10, "RecvBytes": 20}}, 1, "SendBytes", "RecvBytes", false)
	if integer(out["total_send"]) != 10 || integer(out["total_recv"]) != 20 {
		t.Fatal("counter reset deltas")
	}
}
func TestExistingSchemaMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	check(err)
	_, err = db.Exec(`CREATE TABLE VpnSessionSample(VpnSessionSampleID INTEGER PRIMARY KEY,HubName TEXT,UserName TEXT,SessionKey TEXT,ClientIP TEXT,ClientHostname TEXT,StartedDate TEXT,EndedDate TEXT,LastSeenDate TEXT,BytesTotal INTEGER,PacketsTotal INTEGER,UNIQUE(HubName,SessionKey));INSERT INTO VpnSessionSample VALUES(1,'VPN','Alice','key','','','2026-01-01',NULL,'2026-01-01',20,1)`)
	check(err)
	db.Close()
	migrated, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	row := one(migrated, `SELECT * FROM VpnSessionSample WHERE VpnSessionSampleID=1`)
	if integer(row["BytesTotal"]) != 20 || integer(row["DownloadBytes"]) != 0 {
		t.Fatal("migration lost session data")
	}
}
func TestLegacyServerMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	check(err)
	_, err = db.Exec(`CREATE TABLE Server(ServerID INTEGER PRIMARY KEY,Host TEXT,Port INTEGER,EncryptedPassword TEXT,IsDeleted INTEGER);
INSERT INTO Server VALUES(1,'vpn.internal',5555,'legacy-encrypted-password',0);
CREATE TABLE HubTrafficSample(ServerID INTEGER NOT NULL);
CREATE TABLE UserTrafficSample(ServerID INTEGER NOT NULL);`)
	check(err)
	db.Close()
	for range 2 {
		migrated, err := openDB(path)
		if err != nil {
			t.Fatal(err)
		}
		if str(one(migrated, `SELECT SettingValue FROM Setting WHERE SettingKey='se_host'`)["SettingValue"]) != `"vpn.internal"` {
			t.Fatal("old connection not carried forward")
		}
		if str(one(migrated, `SELECT SettingValue FROM Setting WHERE SettingKey='se_password'`)["SettingValue"]) != `"legacy-encrypted-password"` {
			t.Fatal("old encrypted password must remain unchanged")
		}
		execute(migrated, `INSERT INTO HubTrafficSample(HubName,SampledDate) VALUES('VPN','2026-01-01')`)
		migrated.Close()
	}
}

func TestSchemaFailureRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	db, err := sql.Open("sqlite", path)
	check(err)
	_, err = db.Exec(`CREATE TABLE TrafficQuota(Broken INTEGER); CREATE TABLE UserTrafficSample(ServerID INTEGER);`)
	check(err)
	db.Close()
	done := make(chan error, 1)
	go func() {
		db, err := openDB(path)
		if db != nil {
			db.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("malformed schema accepted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("database failure deadlocked before transaction rollback")
	}
	db, err = sql.Open("sqlite", path)
	check(err)
	defer db.Close()
	// Changes before the failing index creation must have rolled back.
	if len(query(db, `PRAGMA table_info(UserTrafficSample)`)) != 1 {
		t.Fatal("failed migration changed the original schema")
	}
}
func TestRPCProtocolErrors(t *testing.T) {
	for _, id := range []string{`"9999"`, `9999`, `1`, `null`, `""`} {
		t.Run(id, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, id)
			}))
			defer server.Close()
			c := NewRPCClient("localhost", 1, "", "header")
			c.http = server.Client()
			c.url = server.URL
			defer c.Close()
			_, err := c.Call(context.Background(), "GetServerInfo", nil)
			if err == nil {
				t.Fatal("invalid response ID accepted")
			}
		})
	}
}
func TestWorkerShutdown(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	a.worker(ctx, time.Millisecond, func() time.Duration { return time.Millisecond }, func(context.Context) {})
	cancel()
	done := make(chan bool)
	go func() { a.workers.Wait(); done <- true }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background worker did not shut down")
	}
}
