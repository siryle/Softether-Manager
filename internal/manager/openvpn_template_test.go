package manager

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func zipOf(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		check(err)
		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	check(zw.Close())
	return buf.Bytes()
}

func TestOpenVPNTemplatePrefersTheRemoteAccessClient(t *testing.T) {
	buf := zipOf(t, map[string]string{
		"hostopenvpn_site_to_site_bridge_l2.ovpn": "dev tap\n",
		"hostopenvpn_remote_access_l3.ovpn":       "client\nremote vpn.example.com 1194\n",
		"readme.txt":                              "not a config",
	})
	name, config, err := openvpnTemplate(buf)
	if err != nil {
		t.Fatal(err)
	}
	if name != "hostopenvpn_remote_access_l3.ovpn" {
		t.Fatalf("picked %q", name)
	}
	if !strings.Contains(config, "remote vpn.example.com 1194") {
		t.Fatalf("config body: %q", config)
	}
}

func TestOpenVPNTemplateFallsBackToAnyOvpn(t *testing.T) {
	name, config, err := openvpnTemplate(zipOf(t, map[string]string{"client.ovpn": "client\n"}))
	if err != nil || name != "client.ovpn" || config != "client\n" {
		t.Fatalf("%q %q %v", name, config, err)
	}
}

func TestOpenVPNTemplateRejectsAnArchiveWithoutConfigs(t *testing.T) {
	if _, _, err := openvpnTemplate(zipOf(t, map[string]string{"readme.txt": "x"})); err == nil {
		t.Fatal("accepted an archive with no .ovpn file")
	}
	if _, _, err := openvpnTemplate([]byte("not a zip")); err == nil {
		t.Fatal("accepted a non-archive")
	}
}

func TestApplyOpenVPNEndpointRewritesRemoteLines(t *testing.T) {
	in := "#remote commented.example 1194\r\n;remote semi.example 1194\r\nremote vpn123.softether.net 1194 udp\r\nproto udp\r\n"
	got := applyOpenVPNEndpoint(in, "vpn.example.com", 443)
	want := "#remote commented.example 1194\r\n;remote semi.example 1194\r\nremote vpn.example.com 443 udp\r\nproto udp\r\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestApplyOpenVPNEndpointPortOnlyAndValidation(t *testing.T) {
	in := "client\nremote auto.softether.net 1194\n"
	if got := applyOpenVPNEndpoint(in, "vpn.example.com", 0); got != "client\nremote vpn.example.com 1194\n" {
		t.Fatalf("host-only rewrite should preserve OpenVPN port, got: %q", got)
	}
	if got := applyOpenVPNEndpoint(in, "", 8443); got != "client\nremote auto.softether.net 8443\n" {
		t.Fatalf("port-only rewrite mismatch: %q", got)
	}
	if got := applyOpenVPNEndpoint(in, "bad host", 0); got != in {
		t.Fatalf("expected invalid host/port to leave template unchanged, got %q", got)
	}
}

func TestOpenVPN5xPortFallbackAndSampleSync(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var mu sync.Mutex
	config := wgTestConfig5x
	enableOpenVPN := true
	enableSSTP := false

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
		case "GetOpenVpnSstpConfig":
			// Simulate SoftEther 5.x returning an empty OpenVPNPortList_str.
			result = M{
				"EnableOpenVPN_bool":  enableOpenVPN,
				"OpenVPNPortList_str": "",
				"EnableSSTP_bool":     enableSSTP,
			}
		case "SetOpenVpnSstpConfig":
			p := object(req["params"])
			enableOpenVPN = boolean(p["EnableOpenVPN_bool"])
			enableSSTP = boolean(p["EnableSSTP_bool"])
			result = M{
				"EnableOpenVPN_bool":  enableOpenVPN,
				"OpenVPNPortList_str": "",
				"EnableSSTP_bool":     enableSSTP,
			}
		case "GetConfig":
			result["FileName_str"] = "vpn_server.config"
			result["FileData_bin"] = base64.StdEncoding.EncodeToString([]byte(config))
		case "SetConfig":
			raw, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["FileData_bin"]))
			config = string(raw)
		case "MakeOpenVpnConfigFile":
			result["Buffer_bin"] = base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{
				"hostopenvpn_remote_access_l3.ovpn": "client\nremote vpn123.softether.net 1194\n",
			}))
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	// 1. Empty 5.x port list defaults to "1194".
	status, out, _ := call(t, a, "GET", "/api/v1/server/openvpn", nil, token)
	if status != 200 || str(out["OpenVPNPortList_str"]) != "1194" {
		t.Fatalf("expected 5.x OpenVPNPortList_str fallback 1194, got %d %v", status, out)
	}

	// 2. Saving a custom port persists it, syncs PortsUDP, and updates .ovpn downloads.
	status, out, _ = call(t, a, "PUT", "/api/v1/server/openvpn", M{
		"EnableOpenVPN_bool":  true,
		"OpenVPNPortList_str": "1195",
		"EnableSSTP_bool":     true,
	}, token)
	if status != 200 || str(out["OpenVPNPortList_str"]) != "1195" {
		t.Fatalf("PUT /server/openvpn failed: %d %v", status, out)
	}

	parsed, err := parseWireGuardState(config)
	if err != nil {
		t.Fatal(err)
	}
	if !containsPort(parsed.UDPPorts, 1195) {
		t.Fatalf("expected 1195 synced to PortsUDP, got %v", parsed.UDPPorts)
	}

	status, out, _ = call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 || !strings.Contains(str(out["config"]), "remote vpn.example.com 1195") {
		t.Fatalf("expected .ovpn to use vpn.example.com 1195, got %d %v", status, out)
	}
}
