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

const wgTestServerPrivKey = "oN8v4lP4wX9z8J0l+W5V1Q9m3kP4rT8uV2wX6yZ0bFE="

const wgTestConfig5x = "\ufeff# Software Configuration File\r\n" +
	"declare root\r\n" +
	"{\r\n" +
	"\tdeclare DDnsClient\r\n" +
	"\t{\r\n" +
	"\t\tbool Disabled false\r\n" +
	"\t}\r\n" +
	"\tdeclare ServerConfiguration\r\n" +
	"\t{\r\n" +
	"\t\tstring PortsUDP 443,$20992,$201194,$205555\r\n" +
	"\r\n" +
	"\t\tdeclare Proto\r\n" +
	"\t\t{\r\n" +
	"\t\t\tdeclare OpenVPN\r\n" +
	"\t\t\t{\r\n" +
	"\t\t\t\tbool Enabled true\r\n" +
	"\t\t\t}\r\n" +
	"\t\t\tdeclare WireGuard\r\n" +
	"\t\t\t{\r\n" +
	"\t\t\t\tbool Enabled false\r\n" +
	"\t\t\t\tstring PresharedKey $\r\n" +
	"\t\t\t\tstring PrivateKey " + wgTestServerPrivKey + "\r\n" +
	"\t\t\t}\r\n" +
	"\t\t}\r\n" +
	"\t}\r\n" +
	"\tdeclare VirtualHUB\r\n" +
	"\t{\r\n" +
	"\t\tdeclare DEFAULT\r\n" +
	"\t\t{\r\n" +
	"\t\t\tbool Online true\r\n" +
	"\t\t\tdeclare Option\r\n" +
	"\t\t\t{\r\n" +
	"\t\t\t\tstring DefaultGateway 192.168.30.1\r\n" +
	"\t\t\t\tstring DefaultSubnet 255.255.255.0\r\n" +
	"\t\t\t\tuint MaxSession 0\r\n" +
	"\t\t\t}\r\n" +
	"\t\t}\r\n" +
	"\t}\r\n" +
	"}\r\n"

const wgTestConfig4x = "\ufeff# Software Configuration File\r\n" +
	"declare root\r\n" +
	"{\r\n" +
	"\tdeclare ServerConfiguration\r\n" +
	"\t{\r\n" +
	"\t\tstring OpenVPNObfuscationMask $\r\n" +
	"\t}\r\n" +
	"}\r\n"

func TestWireGuardConfigParsingAndPatching(t *testing.T) {
	state4, err := parseWireGuardState(wgTestConfig4x)
	if err != nil {
		t.Fatalf("parse 4x config: %v", err)
	}
	if state4.Supported {
		t.Fatalf("4x config should not be reported as WireGuard-supported")
	}

	state5, err := parseWireGuardState(wgTestConfig5x)
	if err != nil {
		t.Fatalf("parse 5x config: %v", err)
	}
	if !state5.Supported || state5.Enabled {
		t.Fatalf("unexpected 5x initial state: %+v", state5)
	}
	expectedPub, err := wgPublicKey(wgTestServerPrivKey)
	if err != nil || state5.PublicKey != expectedPub {
		t.Fatalf("derived public key mismatch: got=%q want=%q err=%v", state5.PublicKey, expectedPub, err)
	}
	if len(state5.UDPPorts) != 4 || state5.UDPPorts[3] != 5555 {
		t.Fatalf("unexpected UDP ports: %v", state5.UDPPorts)
	}
	if len(state5.HubNetworks) != 1 || state5.HubNetworks[0].Gateway != "192.168.30.1" {
		t.Fatalf("unexpected hub networks: %+v", state5.HubNetworks)
	}

	// Enable WireGuard, add 51820, set PresharedKey
	enabled := true
	psk, _ := wgGenerateRandomKey()
	patched, err := patchWireGuardServer(wgTestConfig5x, wgServerPatch{
		Enabled:      &enabled,
		UDPPorts:     []int{443, 992, 1194, 5555, 51820},
		HasUDPPorts:  true,
		PresharedKey: &psk,
	})
	if err != nil {
		t.Fatalf("patch server: %v", err)
	}
	if !strings.Contains(patched, "bool Disabled false\r\n") {
		t.Fatalf("unrelated DDnsClient block altered:\n%s", patched)
	}
	if !strings.Contains(patched, "string PortsUDP 443,$20992,$201194,$205555,$2051820\r\n") {
		t.Fatalf("PortsUDP not escaped as SoftEther expects:\n%s", patched)
	}

	// Add peer when WireGuardKeyList does not yet exist
	_, peerPub, err := wgGenerateKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	patched, err = patchWireGuardAddPeer(patched, peerPub, "DEFAULT", "alice")
	if err != nil {
		t.Fatalf("add peer: %v", err)
	}
	if _, err := patchWireGuardAddPeer(patched, peerPub, "DEFAULT", "alice"); err == nil {
		t.Fatalf("expected duplicate public key error")
	}

	// Patch Hub static network
	patched, err = patchWireGuardHubNetwork(patched, "DEFAULT", "10.88.0.1", "255.255.255.0")
	if err != nil {
		t.Fatalf("patch hub network: %v", err)
	}

	after, err := parseWireGuardState(patched)
	if err != nil {
		t.Fatalf("re-parse patched: %v", err)
	}
	if !after.Enabled || after.PresharedKey != psk || len(after.UDPPorts) != 5 || after.UDPPorts[4] != 51820 {
		t.Fatalf("patched state mismatch: %+v", after)
	}
	if len(after.Peers) != 1 || after.Peers[0].PublicKey != peerPub || after.Peers[0].Hub != "DEFAULT" || after.Peers[0].User != "alice" {
		t.Fatalf("patched peers mismatch: %+v", after.Peers)
	}
	if after.HubNetworks[0].Gateway != "10.88.0.1" {
		t.Fatalf("patched hub network mismatch: %+v", after.HubNetworks)
	}

	// Delete peer
	patched, err = patchWireGuardDeletePeer(patched, peerPub)
	if err != nil {
		t.Fatalf("delete peer: %v", err)
	}
	afterDel, err := parseWireGuardState(patched)
	if err != nil {
		t.Fatalf("re-parse after delete: %v", err)
	}
	if len(afterDel.Peers) != 0 {
		t.Fatalf("expected 0 peers after delete, got %+v", afterDel.Peers)
	}
}

func TestSuggestPeerIP(t *testing.T) {
	got := suggestPeerIP("192.168.30.1", "255.255.255.0", []string{"192.168.30.2", "192.168.30.3"})
	if got != "192.168.30.4" {
		t.Fatalf("got=%q want=192.168.30.4", got)
	}
	if bad := suggestPeerIP("0.0.0.0", "255.255.255.0", nil); bad != "" {
		t.Fatalf("expected empty suggestion for zero gateway, got %q", bad)
	}
}

func TestWireGuardEndpoints(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var mu sync.Mutex
	config := wgTestConfig5x
	saves := 0
	dropOnSetConfig := false

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result := M{}
		var rpcErr M
		switch str(req["method"]) {
		case "GetConfig":
			result["FileName_str"] = "vpn_server.config"
			result["FileData_bin"] = base64.StdEncoding.EncodeToString([]byte(config))
		case "SetConfig":
			raw, err := base64.StdEncoding.DecodeString(str(object(req["params"])["FileData_bin"]))
			if err != nil {
				t.Error(err)
			}
			config = string(raw)
			saves++
			if dropOnSetConfig {
				// Simulate SoftEther restarting and dropping the TCP connection mid-reply.
				hj, ok := w.(http.Hijacker)
				if ok {
					conn, _, herr := hj.Hijack()
					if herr == nil {
						conn.Close()
						return
					}
				}
			}
		case "GetUser":
			p := object(req["params"])
			if str(p["Name_str"]) != "alice" {
				rpcErr = M{"code": 1, "message": "USER_NOT_FOUND"}
			} else {
				result["Name_str"] = "alice"
			}
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		out := M{"jsonrpc": "2.0", "id": req["id"], "result": result}
		if rpcErr != nil {
			out = M{"jsonrpc": "2.0", "id": req["id"], "error": rpcErr}
		}
		writeJSON(w, 200, out)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	// 1. Read initial state
	status, out, _ := call(t, a, "GET", "/api/v1/server/wireguard", nil, token)
	if status != 200 || out["supported"] != true || out["enabled"] != false {
		t.Fatalf("initial GET: %d %v", status, out)
	}

	// 2. Reject missing confirm_restart
	status, _, _ = call(t, a, "PUT", "/api/v1/server/wireguard", M{"enabled": true}, token)
	if status != 422 {
		t.Fatalf("missing confirm_restart should fail 422, got %d", status)
	}

	// 3. Enable WireGuard + UDP 51820 + generate PSK
	status, out, _ = call(t, a, "PUT", "/api/v1/server/wireguard", M{
		"confirm_restart":         true,
		"enabled":                 true,
		"udp_ports":               []int{443, 992, 1194, 5555, 51820},
		"regenerate_preshared_key": true,
	}, token)
	if status != 200 || out["restarting"] != true {
		t.Fatalf("PUT wireguard: %d %v", status, out)
	}

	// 4. Add peer with auto-generated keypair while simulating TransportError on SetConfig
	mu.Lock()
	dropOnSetConfig = true
	mu.Unlock()
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/DEFAULT/wireguard/peers", M{
		"confirm_restart": true,
		"user":            "alice",
		"generate":        true,
		"label":           "Laptop",
		"endpoint_host":   "vpn.example.com",
	}, token)
	if status != 200 || str(out["client_private_key"]) == "" {
		t.Fatalf("POST peer with restart drop failed: %d %v", status, out)
	}
	peer := object(out["peer"])
	pubKey := str(peer["public_key"])
	clientPriv := str(out["client_private_key"])
	if str(peer["assigned_ip"]) != "192.168.30.2" || peer["has_private_key"] != true {
		t.Fatalf("expected auto-assigned IP 192.168.30.2 with has_private_key=true, got %v", peer)
	}
	conf := str(out["client_config"])
	if !strings.Contains(conf, "Endpoint = vpn.example.com:51820") || !strings.Contains(conf, "Address = 192.168.30.2/32") || !strings.Contains(conf, "PresharedKey = ") {
		t.Fatalf("unexpected generated client config:\n%s", conf)
	}

	mu.Lock()
	dropOnSetConfig = false
	mu.Unlock()

	// 5. Update metadata (no restart)
	status, out, _ = call(t, a, "PUT", "/api/v1/hubs/DEFAULT/wireguard/peers/meta", M{
		"public_key":  pubKey,
		"label":       "Work Laptop",
		"assigned_ip": "192.168.30.25",
	}, token)
	if status != 200 || str(out["label"]) != "Work Laptop" || str(out["assigned_ip"]) != "192.168.30.25" || out["has_private_key"] != true {
		t.Fatalf("PUT meta: %d %v", status, out)
	}

	// 6. Render config preview without re-sending private key; decrypted key must be included
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/DEFAULT/wireguard/config", M{
		"public_key": pubKey,
	}, token)
	if status != 200 || out["has_private_key"] != true ||
		!strings.Contains(str(out["config"]), "Address = 192.168.30.25/32") ||
		!strings.Contains(str(out["config"]), "PrivateKey = "+clientPriv) {
		t.Fatalf("POST config preview: %d %v", status, out)
	}

	// 6b. Rotate peer keypair preserves label and assigned_ip and saves the new private key
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/DEFAULT/wireguard/peers/rotate", M{
		"confirm_restart": true,
		"public_key":      pubKey,
	}, token)
	if status != 200 || out["restarting"] != true || str(out["client_private_key"]) == "" || str(out["client_private_key"]) == clientPriv {
		t.Fatalf("POST rotate peer: %d %v", status, out)
	}
	rotatedPeer := object(out["peer"])
	pubKey = str(rotatedPeer["public_key"])
	rotatedPriv := str(out["client_private_key"])
	if str(rotatedPeer["label"]) != "Work Laptop" || str(rotatedPeer["assigned_ip"]) != "192.168.30.25" || rotatedPeer["has_private_key"] != true {
		t.Fatalf("rotated peer lost metadata: %v", rotatedPeer)
	}
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/DEFAULT/wireguard/config", M{
		"public_key": pubKey,
	}, token)
	if status != 200 || !strings.Contains(str(out["config"]), "PrivateKey = "+rotatedPriv) {
		t.Fatalf("POST config after rotate: %d %v", status, out)
	}

	// Reject mismatched client_private_key on meta update
	status, _, _ = call(t, a, "PUT", "/api/v1/hubs/DEFAULT/wireguard/peers/meta", M{
		"public_key":         pubKey,
		"label":              "Work Laptop",
		"assigned_ip":        "192.168.30.25",
		"client_private_key": clientPriv,
	}, token)
	if status != 422 {
		t.Fatalf("expected 422 on mismatched client_private_key, got %d", status)
	}

	// 7. Update hub static network
	status, out, _ = call(t, a, "PUT", "/api/v1/hubs/DEFAULT/wireguard/network", M{
		"confirm_restart": true,
		"gateway":         "10.66.0.1",
		"subnet":          "255.255.255.0",
	}, token)
	if status != 200 || out["restarting"] != true {
		t.Fatalf("PUT hub network: %d %v", status, out)
	}

	// 8. Delete peer
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/DEFAULT/wireguard/peers/delete", M{
		"confirm_restart": true,
		"public_key":      pubKey,
	}, token)
	if status != 200 || out["restarting"] != true {
		t.Fatalf("POST delete peer: %d %v", status, out)
	}

	// Verify 4.x config reports supported=false
	mu.Lock()
	config = wgTestConfig4x
	mu.Unlock()
	status, out, _ = call(t, a, "GET", "/api/v1/server/wireguard", nil, token)
	if status != 200 || out["supported"] != false {
		t.Fatalf("expected supported=false for 4x config: %d %v", status, out)
	}
	if saves != 5 {
		t.Fatalf("expected 5 config saves, got %d", saves)
	}
}

func TestWireGuardPortAutoListeners(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var mu sync.Mutex
	config := wgTestConfig5x

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result := M{}
		switch str(req["method"]) {
		case "GetConfig":
			result["FileName_str"] = "vpn_server.config"
			result["FileData_bin"] = base64.StdEncoding.EncodeToString([]byte(config))
		case "SetConfig":
			raw, err := base64.StdEncoding.DecodeString(str(object(req["params"])["FileData_bin"]))
			if err != nil {
				t.Error(err)
			}
			config = string(raw)
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	// Initial state defaults to 51820 even when PortsUDP has 443/992/1194/5555.
	status, out, _ := call(t, a, "GET", "/api/v1/server/wireguard", nil, token)
	if status != 200 || int(integer(out["wireguard_port"])) != 51820 || out["wireguard_port_listening"] != false {
		t.Fatalf("unexpected initial wg port state: %d %v", status, out)
	}

	// Setting wireguard_port=51820 automatically adds 51820 to PortsUDP while keeping 443/992/1194/5555.
	status, out, _ = call(t, a, "PUT", "/api/v1/server/wireguard", M{
		"confirm_restart": true,
		"wireguard_port":  51820,
	}, token)
	if status != 200 || out["restarting"] != true {
		t.Fatalf("PUT wireguard_port=51820: %d %v", status, out)
	}

	status, out, _ = call(t, a, "GET", "/api/v1/server/wireguard", nil, token)
	if status != 200 || int(integer(out["wireguard_port"])) != 51820 || out["wireguard_port_listening"] != true {
		t.Fatalf("expected wireguard_port=51820 listening, got %d %v", status, out)
	}

	// Changing from 51820 -> 51822 replaces 51820 with 51822 in PortsUDP while keeping 443/992/1194/5555.
	status, out, _ = call(t, a, "PUT", "/api/v1/server/wireguard", M{
		"confirm_restart": true,
		"wireguard_port":  51822,
	}, token)
	if status != 200 || out["restarting"] != true {
		t.Fatalf("PUT wireguard_port=51822: %d %v", status, out)
	}
	parsed, err := parseWireGuardState(config)
	if err != nil {
		t.Fatal(err)
	}
	if containsPort(parsed.UDPPorts, 51820) || !containsPort(parsed.UDPPorts, 51822) || !containsPort(parsed.UDPPorts, 443) {
		t.Fatalf("unexpected PortsUDP after wg port switch: %v", parsed.UDPPorts)
	}
}
