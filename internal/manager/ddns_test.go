package manager

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const ddnsTestConfig = "\ufeff# Configuration\r\ndeclare root\r\n{\r\n\tdeclare DDnsClient\r\n\t{\r\n\t\tbool Disabled false\r\n\t\tbyte Key abc123\r\n\t}\r\n\tdeclare VirtualHUB\r\n\t{\r\n\t\tbool Disabled true\r\n\t}\r\n}\r\n"

func TestDDNSConfigValue(t *testing.T) {
	start, end, disabled, err := ddnsConfigValue(ddnsTestConfig)
	if err != nil || disabled || ddnsTestConfig[start:end] != "false" {
		t.Fatalf("config value: %d %d %t %v", start, end, disabled, err)
	}
	updated := ddnsTestConfig[:start] + "true" + ddnsTestConfig[end:]
	_, _, disabled, err = ddnsConfigValue(updated)
	if err != nil || !disabled {
		t.Fatalf("updated value: %t %v", disabled, err)
	}
	for _, config := range []string{
		"", "declare root\n{\n}\n",
		strings.Replace(ddnsTestConfig, "bool Disabled false", "bool Disabled maybe", 1),
		strings.Replace(ddnsTestConfig, "bool Disabled false", "uint Disabled 0", 1),
		strings.Replace(ddnsTestConfig, "bool Disabled false", "bool Disabled false\n bool Disabled true", 1),
		strings.Replace(ddnsTestConfig, "declare DDnsClient", "declare Other", 1),
		strings.Replace(ddnsTestConfig, "declare VirtualHUB", "declare DDnsClient", 1),
		strings.TrimSuffix(ddnsTestConfig, "}\r\n"),
		ddnsTestConfig + "}\n",
	} {
		if _, _, _, err := ddnsConfigValue(config); err == nil {
			t.Fatalf("accepted invalid/ambiguous config: %q", config)
		}
	}
}

func TestDDNSSwitch(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var mu sync.Mutex
	config := ddnsTestConfig
	saves := 0
	unsupported := false
	rpcFailure := ""
	configFailure := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result := M{}
		var rpcError M
		switch str(req["method"]) {
		case "GetConfig":
			result["FileData_bin"] = base64.StdEncoding.EncodeToString([]byte(config))
			if configFailure != "" {
				rpcError = M{"code": 1, "message": configFailure}
			}
		case "SetConfig":
			raw, err := base64.StdEncoding.DecodeString(str(object(req["params"])["FileData_bin"]))
			if err != nil {
				t.Error(err)
			}
			config = string(raw)
			saves++
		case "GetDDnsClientStatus":
			_, _, disabled, _ := ddnsConfigValue(config)
			if rpcFailure != "" {
				rpcError = M{"code": 1, "message": rpcFailure}
			} else if disabled || unsupported {
				rpcError = M{"code": 33, "message": "Error code 33: Unsupported."}
			} else {
				result["CurrentFqdn_str"] = "vpn-test.softether.net"
			}
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		out := M{"jsonrpc": "2.0", "id": req["id"], "result": result}
		if rpcError != nil {
			out = M{"jsonrpc": "2.0", "id": req["id"], "error": rpcError}
		}
		writeJSON(w, 200, out)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	status, out, _ := call(t, a, "GET", "/api/v1/server/ddns", nil, token)
	if status != 200 || out["enabled"] != true || out["CurrentFqdn_str"] != "vpn-test.softether.net" {
		t.Fatalf("enabled status: %d %v", status, out)
	}
	status, out, _ = call(t, a, "PUT", "/api/v1/server/ddns", M{"enabled": false, "confirm_restart": true}, token)
	if status != 409 {
		t.Fatalf("unsafe toggle should require maintenance: %d %v", status, out)
	}
	mu.Lock()
	if saves != 0 || config != ddnsTestConfig {
		t.Fatalf("configuration changed: %d %q", saves, config)
	}
	config = strings.Replace(ddnsTestConfig, "Disabled false", "Disabled true", 1)
	mu.Unlock()
	status, out, _ = call(t, a, "GET", "/api/v1/server/ddns", nil, token)
	if status != 200 || out["enabled"] != false {
		t.Fatalf("disabled status: %d %v", status, out)
	}
	for _, body := range []M{{"enabled": false}, {"enabled": "false", "confirm_restart": true}, {"confirm_restart": true}} {
		status, _, _ = call(t, a, "PUT", "/api/v1/server/ddns", body, token)
		if status != 422 {
			t.Fatalf("invalid body accepted: %d %v", status, body)
		}
	}
	status, _, _ = call(t, a, "PUT", "/api/v1/server/ddns", M{"enabled": false, "confirm_restart": true}, "")
	if status != 401 {
		t.Fatalf("unauthenticated toggle: %d", status)
	}
	mu.Lock()
	unsupported = true
	config = ddnsTestConfig
	mu.Unlock()
	status, out, _ = call(t, a, "GET", "/api/v1/server/ddns", nil, token)
	if status != 400 || object(out["detail"])["se_error"] != "UnsupportedError" {
		t.Fatalf("unsupported capability mistaken for disabled DDNS: %d %v", status, out)
	}
	mu.Lock()
	configFailure = "ERR_ACCESS_DENIED"
	mu.Unlock()
	status, out, _ = call(t, a, "GET", "/api/v1/server/ddns", nil, token)
	if status != 502 || object(out["detail"])["se_error"] != "AuthenticationError" {
		t.Fatalf("configuration read error hidden: %d %v", status, out)
	}
	mu.Lock()
	configFailure = ""
	rpcFailure = "ERR_ACCESS_DENIED"
	config = strings.Replace(ddnsTestConfig, "Disabled false", "Disabled true", 1)
	mu.Unlock()
	status, out, _ = call(t, a, "GET", "/api/v1/server/ddns", nil, token)
	if status != 502 || object(out["detail"])["se_error"] != "AuthenticationError" {
		t.Fatalf("authentication error hidden: %d %v", status, out)
	}
	mu.Lock()
	config = "invalid config"
	mu.Unlock()
	status, _, _ = call(t, a, "PUT", "/api/v1/server/ddns", M{"enabled": false, "confirm_restart": true}, token)
	if status != 409 {
		t.Fatalf("unexpected toggle result: %d", status)
	}
	mu.Lock()
	defer mu.Unlock()
	if saves != 0 {
		t.Fatalf("unsafe request wrote the configuration: %d", saves)
	}
}
