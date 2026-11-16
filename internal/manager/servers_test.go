package manager

import (
	"encoding/json"
	"net/http/httptest"
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
