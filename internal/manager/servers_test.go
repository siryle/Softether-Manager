package manager

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func parseArray(rec *httptest.ResponseRecorder) []M {
	var out []M
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

func TestServersCRUD(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// 1. List servers (default server should exist from bootstrap/migration)
	status, _, rec := call(t, a, "GET", "/api/v1/servers", nil, token)
	if status != 200 {
		t.Fatalf("list servers status %d", status)
	}
	servers := parseArray(rec)
	if len(servers) != 1 {
		t.Fatalf("expected 1 initial server, got %d", len(servers))
	}
	defaultID := integer(servers[0]["id"])
	if !boolean(servers[0]["is_default"]) {
		t.Fatalf("expected initial server to be default")
	}

	// 2. Add second server
	status, newSrv, _ := call(t, a, "POST", "/api/v1/servers", M{
		"name":      "Tokyo Node",
		"host":      "192.168.1.10",
		"port":      5555,
		"password":  "testpass",
		"skip_test": true,
	}, token)
	if status != 200 {
		t.Fatalf("create server status %d: %v", status, newSrv)
	}
	tokyoID := integer(newSrv["id"])
	if tokyoID == defaultID {
		t.Fatalf("expected new ID for second server, got %d", tokyoID)
	}

	// 3. List should now show 2 servers
	status, _, rec = call(t, a, "GET", "/api/v1/servers", nil, token)
	if status != 200 || len(parseArray(rec)) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(parseArray(rec)))
	}

	// 4. Update second server
	status, updated, _ := call(t, a, "PUT", "/api/v1/servers/"+str(tokyoID), M{
		"name": "Tokyo Edge",
	}, token)
	if status != 200 || str(updated["name"]) != "Tokyo Edge" {
		t.Fatalf("update server failed: %d %v", status, updated)
	}

	// 5. Set second server as default
	status, defOut, _ := call(t, a, "POST", "/api/v1/servers/"+str(tokyoID)+"/set-default", nil, token)
	if status != 200 || !boolean(defOut["ok"]) {
		t.Fatalf("set default failed: %d %v", status, defOut)
	}
	row := one(a.db, `SELECT IsDefault FROM Server WHERE ServerID=?`, tokyoID)
	if !boolean(row["IsDefault"]) {
		t.Fatal("Tokyo server should now be default in DB")
	}
	rowDefault := one(a.db, `SELECT IsDefault FROM Server WHERE ServerID=?`, defaultID)
	if boolean(rowDefault["IsDefault"]) {
		t.Fatal("old default server should no longer be default in DB")
	}

	// 6. Delete the first server
	status, delOut, _ := call(t, a, "DELETE", "/api/v1/servers/"+str(defaultID), nil, token)
	if status != 200 || !boolean(delOut["ok"]) {
		t.Fatalf("delete server failed: %d %v", status, delOut)
	}

	// 7. Attempting to delete the only remaining server must fail (cannot delete only server)
	status, failDel, _ := call(t, a, "DELETE", "/api/v1/servers/"+str(tokyoID), nil, token)
	if status != 422 {
		t.Fatalf("deleting only server should fail with 422, got %d %v", status, failDel)
	}
}

func TestServerContextRouting(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	status, newSrv, _ := call(t, a, "POST", "/api/v1/servers", M{
		"name":      "Second Server",
		"host":      "10.0.0.1",
		"port":      5555,
		"password":  "secondpass",
		"skip_test": true,
	}, token)
	if status != 200 {
		t.Fatalf("create server: %d", status)
	}
	secondID := integer(newSrv["id"])

	req := httptest.NewRequest("GET", "/api/v1/server/connection", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Server-ID", str(secondID))
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var out M
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if str(out["name"]) != "Second Server" || str(out["host"]) != "10.0.0.1" {
		t.Fatalf("expected Second Server data, got %v", out)
	}
}

// Both servers deliberately reuse their HUB, user and session names.
func managedFake(t *testing.T, a *App, name string) (int64, *fakeSoftEther) {
	t.Helper()
	fake := &fakeSoftEther{hubOnline: true, userAccess: true, send: 600, recv: 400}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		fake.calls = append(fake.calls, req)
		out := M{"server": name}
		switch str(req["method"]) {
		case "GetServerInfo":
			out = M{"ServerHostName_str": name, "ServerVersionString_str": "test"}
		case "GetServerStatus":
			out = M{"NumSessionsTotal_u32": 1, "NumHubTotal_u32": 1}
		case "EnumHub":
			out = M{"HubList": []M{{"HubName_str": "VPN"}}}
		case "GetHubStatus":
			out = M{"Send.UnicastBytes_u64": fake.send, "Recv.UnicastBytes_u64": fake.recv, "NumUsers_u32": 1, "NumSessions_u32": 1}
		case "EnumUser":
			out = M{"UserList": []M{{"Name_str": "Alice", "Ex.Send.UnicastBytes_u64": fake.send, "Ex.Recv.UnicastBytes_u64": fake.recv}}}
		case "EnumSession":
			out = M{"SessionList": []M{{"Name_str": "SID", "Username_str": "Alice", "UniqueId_bin": "same-key", "CreatedTime_dt": now(), "PacketSize_u64": fake.send + fake.recv}}}
		case "GetUser", "GetSessionStatus":
			out = M{"Name_str": "Alice", "Send.UnicastBytes_u64": fake.send, "Recv.UnicastBytes_u64": fake.recv, "UsePolicy_bool": true, "policy:Access_bool": true}
		}
		writeJSON(w, 200, M{"id": req["id"], "jsonrpc": "2.0", "result": out})
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	id := execute(a.db, `INSERT INTO Server(Name,Host,Port,EncryptedPassword,AuthType,IsEnabled,IsDefault,CreatedDate,UpdatedDate) VALUES(?,?,?,?,'header',1,0,?,?)`, name, target.Hostname(), integer(target.Port()), a.encrypt(""), now(), now())
	return id, fake
}
func callServer(t *testing.T, a *App, id int64, method, path string, body any, token string) (int, M, *httptest.ResponseRecorder) {
	t.Helper()
	var raw io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, raw)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Server-ID", str(id))
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	out := M{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}
func TestSelectedServerNeverFallsBack(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	first, fake := managedFake(t, a, "first")
	second, _ := managedFake(t, a, "second")
	for _, id := range []int64{first, second} {
		status, out, _ := callServer(t, a, id, "GET", "/api/v1/server/info", nil, token)
		if status != 200 || out["ServerHostName_str"] != map[int64]string{first: "first", second: "second"}[id] {
			t.Fatalf("wrong target: %d %v", status, out)
		}
	}
	status, out, _ := callServer(t, a, first, "GET", "/api/v1/server/ddns", nil, token)
	if status != 200 || out["server"] != "first" {
		t.Fatalf("DDNS bypassed selected target: %d %v", status, out)
	}
	execute(a.db, `UPDATE Server SET IsEnabled=0 WHERE ServerID=?`, second)
	for _, tc := range []struct {
		id   int64
		want int
	}{{second, 409}, {99999, 404}, {-1, 422}} {
		status, _, _ = callServer(t, a, tc.id, "GET", "/api/v1/server/info", nil, token)
		if status != tc.want {
			t.Fatalf("id %d: %d, want %d", tc.id, status, tc.want)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 2 {
		t.Fatalf("invalid target reached another server: %v", fake.calls)
	}
}
func TestSelectedConnectionAndPasswordUpdates(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	first, _ := managedFake(t, a, "first")
	second, _ := managedFake(t, a, "second")
	original := a.serverRow(first)
	status, out, _ := callServer(t, a, second, "PUT", "/api/v1/server/admin-password", M{"password": "new-password"}, token)
	if status != 200 {
		t.Fatalf("password update: %d %v", status, out)
	}
	pwd, err := a.decrypt(str(a.serverRow(second)["EncryptedPassword"]))
	if err != nil || pwd != "new-password" {
		t.Fatalf("credential saved to wrong server: %q %v", pwd, err)
	}
	if a.serverRow(first)["EncryptedPassword"] != original["EncryptedPassword"] {
		t.Fatal("other server password changed")
	}
	status, out, _ = callServer(t, a, second, "PUT", "/api/v1/server/connection", M{"host": "updated.example.com", "port": 443, "password": nil, "skip_test": true}, token)
	if status != 200 || out["host"] != "updated.example.com" {
		t.Fatalf("connection update: %d %v", status, out)
	}
	pwd, err = a.decrypt(str(a.serverRow(second)["EncryptedPassword"]))
	if err != nil || pwd != "new-password" {
		t.Fatal("blank update did not retain selected server password")
	}
	if a.serverRow(first)["Host"] != original["Host"] {
		t.Fatal("other server connection changed")
	}
}
func TestMultiServerSamplesQuotasAndCredentials(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	first, fake1 := managedFake(t, a, "first")
	second, fake2 := managedFake(t, a, "second")
	a.saveQuota(first, "user", "VPN", "Alice", 100, "total", true)
	a.saveQuota(second, "user", "VPN", "Alice", 10000, "total", true)
	a.saveCredential(first, "VPN", "Alice", "first-hash")
	a.saveCredential(second, "VPN", "Alice", "second-hash")
	a.sampleTraffic(context.Background())
	a.sampleSessions(context.Background())
	a.quotaTick(context.Background())
	if a.storedCredential(first, "VPN", "Alice") != "first-hash" || a.storedCredential(second, "VPN", "Alice") != "second-hash" {
		t.Fatal("credentials crossed servers")
	}
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample", "VpnSessionSample"} {
		for _, id := range []int64{first, second} {
			if integer(one(a.db, `SELECT COUNT(*) n FROM `+table+` WHERE ServerID=?`, id)["n"]) != 1 {
				t.Fatalf("missing isolated %s for %d", table, id)
			}
		}
	}
	for _, tc := range []struct {
		fake *fakeSoftEther
		want bool
	}{{fake1, true}, {fake2, false}} {
		tc.fake.mu.Lock()
		cut := false
		for _, req := range tc.fake.calls {
			if req["method"] == "DeleteSession" {
				cut = true
			}
		}
		tc.fake.mu.Unlock()
		if cut != tc.want {
			t.Fatalf("quota disconnected wrong server: got %t want %t", cut, tc.want)
		}
	}
	for _, id := range []int64{first, second} {
		status, _, rec := callServer(t, a, id, "GET", "/api/v1/system/audit", nil, token)
		if status != 200 {
			t.Fatal(status)
		}
		for _, row := range parseArray(rec) {
			if row["server_id"] != nil && integer(row["server_id"]) != id {
				t.Fatal("audit log crossed servers")
			}
		}
	}
	session := one(a.db, `SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=?`, first)
	status, _, _ := callServer(t, a, second, "GET", "/api/v1/hubs/VPN/session-history/"+str(session["VpnSessionSampleID"])+"/usage", nil, token)
	if status != 404 {
		t.Fatal("another server's session history was accessible")
	}
	status, out, _ := callServer(t, a, second, "DELETE", "/api/v1/servers/"+str(first), nil, token)
	if status != 200 {
		t.Fatalf("remove: %d %v", status, out)
	}
	if integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionTrafficSample WHERE VpnSessionSampleID=?`, session["VpnSessionSampleID"])["n"]) != 0 {
		t.Fatal("orphaned session traffic after removal")
	}
	if a.storedCredential(second, "VPN", "Alice") != "second-hash" {
		t.Fatal("removal affected another server")
	}
}
func TestSingleServerUniqueConstraintsMigrateWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "single.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := regexp.MustCompile(`(?m)^\s*"ServerID"\s+INTEGER NOT NULL DEFAULT 1[^\n]*\n`).ReplaceAllString(schema, "")
	old = strings.ReplaceAll(old, `"ServerID", `, "")
	if _, err = db.Exec(old); err != nil {
		t.Fatal(err)
	}
	execute(db, `INSERT INTO Server(ServerID,Name,Host,CreatedDate,UpdatedDate,IsDefault) VALUES(1,'Original','original',?,?,1)`, now(), now())
	execute(db, `INSERT INTO VpnSessionSample(HubName,UserName,SessionKey,StartedDate,LastSeenDate) VALUES('VPN','Alice','same-key',?,?)`, now(), now())
	execute(db, `INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,TotalBytes,SampledDate) VALUES(1,42,?)`, now())
	execute(db, `INSERT INTO VpnUserCredential(HubName,UserName,PasswordHash,UpdatedDate) VALUES('VPN','Alice','preserved',?)`, now())
	execute(db, `INSERT INTO TrafficQuota(SubjectType,HubName,UserName,UserKey,LimitBytes,CycleStartDate,CreatedDate,UpdatedDate) VALUES('user','VPN','Alice','alice',123,?,?,?)`, now(), now(), now())
	db.Close()
	for range 2 {
		db, err = openDB(path)
		if err != nil {
			t.Fatal(err)
		}
		if integer(one(db, `SELECT COUNT(*) n FROM VpnSessionTrafficSample WHERE VpnSessionSampleID=1 AND TotalBytes=42`)["n"]) != 1 {
			t.Fatal("session traffic lost")
		}
		if one(db, `SELECT PasswordHash FROM VpnUserCredential WHERE ServerID=1 AND UserName='Alice'`)["PasswordHash"] != "preserved" {
			t.Fatal("credential lost")
		}
		if integer(one(db, `SELECT LimitBytes FROM TrafficQuota WHERE ServerID=1`)["LimitBytes"]) != 123 {
			t.Fatal("quota lost")
		}
		db.Close()
	}
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execute(db, `INSERT INTO Server(ServerID,Name,Host,CreatedDate,UpdatedDate) VALUES(2,'Second','second',?,?)`, now(), now())
	execute(db, `INSERT INTO VpnUserCredential(ServerID,HubName,UserName,PasswordHash,UpdatedDate) VALUES(2,'VPN','Alice','second',?)`, now())
	execute(db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,StartedDate,LastSeenDate) VALUES(2,'VPN','Alice','same-key',?,?)`, now(), now())
	execute(db, `INSERT INTO TrafficQuota(ServerID,SubjectType,HubName,UserName,UserKey,CycleStartDate,CreatedDate,UpdatedDate) VALUES(2,'user','VPN','Alice','alice',?,?,?)`, now(), now(), now())
}
