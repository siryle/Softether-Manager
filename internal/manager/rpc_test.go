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
	c.http = server.Client()
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

func TestRPCUnsupportedErrors(t *testing.T) {
	for _, tc := range []struct {
		code    int
		message string
		kind    string
		status  int
	}{
		{33, "Error code 33: Unsupported.", "UnsupportedError", 400},
		{-32603, "Error code 33: Unsupported.", "UnsupportedError", 400},
		{33, "ERR_NOT_SUPPORTED", "UnsupportedError", 400},
		{-32601, "Method not found", "UnsupportedError", 400},
		{1, "ERR_ACCESS_DENIED", "AuthenticationError", 502},
		{1, "ERR_OBJECT_NOT_FOUND", "NotFoundError", 404},
		{1, "ERR_BUSY", "RpcError", 400},
	} {
		t.Run(tc.message, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req M
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "error": M{"code": tc.code, "message": tc.message}})
			}))
			defer server.Close()
			c := NewRPCClient("localhost", 1, "", "header")
			c.http = server.Client()
			c.url = server.URL
			defer c.Close()
			_, err := c.Call(context.Background(), "GetAzureStatus", nil)
			got, ok := err.(httpError)
			if !ok || got.Status != tc.status || object(got.Detail)["se_error"] != tc.kind {
				t.Fatalf("error classification: %v; want %s (%d)", err, tc.kind, tc.status)
			}
			if object(got.Detail)["message"] != tc.message || object(got.Detail)["se_code"] != tc.code {
				t.Fatalf("original error was lost: %v", got.Detail)
			}
		})
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

func TestHubLogSettingsSave(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	hub := "Office / 上海"
	path := "/api/v1/hubs/" + url.PathEscape(hub) + "/log-settings"
	for _, packetLog := range []bool{false, true, false} {
		body := M{
			"SaveSecurityLog_bool":      true,
			"SecurityLogSwitchType_u32": 4,
			"SavePacketLog_bool":        packetLog,
			"PacketLogSwitchType_u32":   4,
			"PacketLogConfig_u32":       []int{1, 2, 0, 1, 2, 0, 1, 2, 0, 0, 0, 0, 0, 0, 0, 0},
		}
		status, out, _ := call(t, a, "PUT", path, body, token)
		if status != 200 {
			t.Fatalf("save packetLog=%t: %d %v", packetLog, status, out)
		}
		status, out, _ = call(t, a, "GET", path, nil, token)
		want := clone(body)
		want["HubName_str"] = hub
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(out)
		if status != 200 || string(gotJSON) != string(wantJSON) {
			t.Fatalf("saved settings: %d %s; want %s", status, gotJSON, wantJSON)
		}
	}
	fake.mu.Lock()
	before := len(fake.calls)
	fake.mu.Unlock()
	status, out, _ := call(t, a, "PUT", path, M{"PacketLogConfig_u32": []int{0, -1, 2}}, token)
	if status != 422 {
		t.Fatalf("invalid packet config accepted: %d %v", status, out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != before {
		t.Fatal("invalid packet config sent to SoftEther")
	}
}

func TestListenerToggleReturnsJSON(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	fake.mu.Lock()
	fake.listeners = []M{
		{"Ports_u32": 1194, "Enables_bool": true, "Errors_bool": false},
		{"Ports_u32": 5555, "Enables_bool": true, "Errors_bool": false},
	}
	fake.mu.Unlock()
	for _, enabled := range []bool{false, true, false} {
		status, out, rec := call(t, a, "PUT", "/api/v1/server/listeners/1194", M{"enable": enabled}, token)
		if status != 200 || !json.Valid(rec.Body.Bytes()) {
			t.Fatalf("toggle enabled=%t: status=%d body=%q", enabled, status, rec.Body.String())
		}
		if out["enabled"] != enabled || out["Enables_bool"] != enabled || integer(out["Ports_u32"]) != 1194 {
			t.Fatalf("toggle response: %v", out)
		}
		listeners := list(out["ListenerList"])
		if len(listeners) != 2 || listeners[0]["Enables_bool"] != enabled || listeners[1]["Enables_bool"] != true {
			t.Fatalf("listener states: %v", listeners)
		}
		for _, listener := range listeners {
			if _, exists := listener["ListenerList"]; exists {
				t.Fatal("response metadata was added to a listener in the list")
			}
		}
		status, out, _ = call(t, a, "GET", "/api/v1/server/listeners", nil, token)
		if status != 200 || list(out["ListenerList"])[0]["Enables_bool"] != enabled {
			t.Fatalf("listener readback: %d %v", status, out)
		}
	}
}

func TestUnsignedWireValues(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		valid            bool
	}{
		{"u32 scalar", "Value_u32", `4294967295`, true},
		{"u32 array", "Ports_u32", `[443,992,5555]`, true},
		{"u32 array bounds", "PacketLogConfig_u32", `[0,4294967295]`, true},
		{"empty array", "PacketLogConfig_u32", `[]`, true},
		{"u64 array", "Value_u64", `[0,18446744073709551615]`, true},
		{"u32 overflow", "Value_u32", `4294967296`, false},
		{"u32 array overflow", "PacketLogConfig_u32", `[0,4294967296]`, false},
		{"u64 array overflow", "Value_u64", `[18446744073709551616]`, false},
		{"negative", "PacketLogConfig_u32", `[0,-1]`, false},
		{"fraction", "PacketLogConfig_u32", `[0,1.5]`, false},
		{"text", "PacketLogConfig_u32", `[0,"invalid"]`, false},
		{"boolean", "PacketLogConfig_u32", `[0,true]`, false},
		{"null element", "PacketLogConfig_u32", `[0,null]`, false},
		{"nested array", "PacketLogConfig_u32", `[0,[1]]`, false},
		{"null", "PacketLogConfig_u32", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var value any
			decoder := json.NewDecoder(strings.NewReader(tc.value))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			defer func() {
				err := recover()
				if tc.valid && err != nil {
					t.Fatalf("valid value rejected: %v", err)
				}
				if !tc.valid {
					e, ok := err.(httpError)
					if !ok || e.Status != 422 {
						t.Fatalf("invalid value must return 422: %v", err)
					}
				}
			}()
			validateWire(M{tc.key: value})
		})
	}
}
