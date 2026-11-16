package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestRPCSoftEtherStringIDs(t *testing.T) {
	var ids []string
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		// SoftEther's JsonGetStr/JsonSetStr pair returns "0" for a numeric request ID.
		id, ok := req["id"].(string)
		if !ok {
			id = "0"
		}
		mu.Lock()
		ids = append(ids, id)
		mu.Unlock()
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": id, "result": M{"method": req["method"]}})
	}))
	defer server.Close()
	c := NewRPCClient("localhost", 1, "", "header")
	c.url = server.URL + "/api/"
	defer c.Close()
	for _, method := range []string{"GetServerInfo", "GetServerStatus"} {
		out, err := c.Call(context.Background(), method, nil)
		if err != nil || out["method"] != method {
			t.Fatalf("%s: %v %v", method, out, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(ids, ",") != "1,2" {
		t.Fatalf("request IDs: %v", ids)
	}
}

func TestConnectionFallsBackToBasicAfterHeaderEOF(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var attempts []string
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		username, password, basic := r.BasicAuth()
		mode := "header"
		if basic {
			mode = "basic"
		}
		mu.Lock()
		attempts = append(attempts, mode+":"+str(req["method"]))
		mu.Unlock()
		if _, header := r.Header["X-Vpnadmin-Password"]; header {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		if !basic || username != "administrator" || password != "vpn-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id, ok := req["id"].(string)
		if !ok {
			id = "0"
		}
		out := M{"ServerVersionString_str": "5.02", "ServerHostName_str": "vpnserver"}
		if req["method"] == "GetServerStatus" {
			out = M{"NumSessionsTotal_u32": 1}
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": id, "result": out})
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(target.Port())
	body := M{"host": target.Hostname(), "port": port, "password": "vpn-secret"}
	status, out, _ := call(t, a, "POST", "/api/v1/server/connection/test", body, token)
	if status != 200 || !boolean(out["ok"]) || out["auth"] != "basic" || out["version"] != "5.02" || integer(out["sessions"]) != 1 {
		t.Fatalf("connection test: %d %v", status, out)
	}
	if boolean(a.connection()["configured"]) {
		t.Fatal("connection test saved settings")
	}
	status, out, _ = call(t, a, "PUT", "/api/v1/server/connection", body, token)
	if status != 200 || !boolean(out["configured"]) || out["auth"] != "basic" {
		t.Fatalf("save connection: %d %v", status, out)
	}
	status, out, _ = call(t, a, "POST", "/api/v1/server/probe", nil, token)
	if status != 200 || !boolean(out["online"]) || out["version"] != "5.02" {
		t.Fatalf("saved connection probe: %d %v", status, out)
	}
	mu.Lock()
	defer mu.Unlock()
	want := "header:GetServerInfo,basic:GetServerInfo,basic:GetServerStatus,header:GetServerInfo,basic:GetServerInfo,basic:GetServerStatus,basic:GetServerInfo,basic:GetServerStatus"
	if strings.Join(attempts, ",") != want {
		t.Fatalf("authentication attempts: %v", attempts)
	}
}
