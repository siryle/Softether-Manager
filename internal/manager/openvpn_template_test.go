package manager

import (
	"archive/zip"
	"bytes"
	"strings"
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
