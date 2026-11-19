package manager

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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

func makeTestCert(t *testing.T, cn, org string, isCA bool, parent *x509.Certificate, parentKey *rsa.PrivateKey) (*x509.Certificate, *rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true,
		IsCA:                  isCA,
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	} else {
		tmpl.DNSNames = []string{cn}
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	signerCert := tmpl
	signerKey := key
	if parent != nil && parentKey != nil {
		signerCert = parent
		signerKey = parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return parsed, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSyncSoftEtherChainCertsSeparatesFilesAndCleansStale(t *testing.T) {
	chainDir := filepath.Join(t.TempDir(), "chain_certs")
	cfg := Config{SEChainCertsDir: chainDir}

	rootCert, rootKey, rootPEM := makeTestCert(t, "Test Root", "ISRG", true, nil, nil)
	inter1, inter1Key, inter1PEM := makeTestCert(t, "R10", "Let's Encrypt", true, rootCert, rootKey)
	inter2, inter2Key, inter2PEM := makeTestCert(t, "R11", "Let's Encrypt", true, rootCert, rootKey)
	_, _, leafPEM := makeTestCert(t, "vpn.example.com", "", false, inter1, inter1Key)
	_, _, _ = inter2, inter2Key, inter2PEM

	// Include leaf + 2 intermediates + self-signed root: only the 2 intermediates should be written separately.
	bundle := append(append(append([]byte{}, leafPEM...), inter1PEM...), append(inter2PEM, rootPEM...)...)
	synced, err := syncSoftEtherChainCerts(cfg, "127.0.0.1", bundle)
	if err != nil || !synced {
		t.Fatalf("syncSoftEtherChainCerts failed: synced=%v err=%v", synced, err)
	}
	b1, err := os.ReadFile(filepath.Join(chainDir, "sem-chain-1.cer"))
	if err != nil || !bytes.Equal(b1, inter1PEM) {
		t.Fatalf("sem-chain-1.cer mismatch: err=%v", err)
	}
	b2, err := os.ReadFile(filepath.Join(chainDir, "sem-chain-2.cer"))
	if err != nil || !bytes.Equal(b2, inter2PEM) {
		t.Fatalf("sem-chain-2.cer mismatch: err=%v", err)
	}

	// Shrink to 1 intermediate: stale sem-chain-2.cer must be removed.
	synced, err = syncSoftEtherChainCerts(cfg, "127.0.0.1", append(append([]byte{}, leafPEM...), inter2PEM...))
	if err != nil || !synced {
		t.Fatalf("shrink sync failed: synced=%v err=%v", synced, err)
	}
	if _, err := os.Stat(filepath.Join(chainDir, "sem-chain-2.cer")); !os.IsNotExist(err) {
		t.Fatalf("expected stale sem-chain-2.cer to be removed, got err=%v", err)
	}

	// Switch to self-signed cert (0 intermediates): sem-chain-1.cer must be cleaned up.
	synced, err = syncSoftEtherChainCerts(cfg, "127.0.0.1", rootPEM)
	if err != nil || synced {
		t.Fatalf("expected synced=false for self-signed cert, got synced=%v err=%v", synced, err)
	}
	if _, err := os.Stat(filepath.Join(chainDir, "sem-chain-1.cer")); !os.IsNotExist(err) {
		t.Fatalf("expected sem-chain-1.cer to be removed on self-signed cert, got err=%v", err)
	}
}

func TestOpenVPNCARewriteForLetsEncryptAndRenewalStability(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	chainDir := filepath.Join(t.TempDir(), "chain_certs")
	a.cfg.SEChainCertsDir = chainDir

	rootCert, rootKey, rootPEM := makeTestCert(t, "Test Root", "ISRG", true, nil, nil)
	inter1, inter1Key, inter1PEM := makeTestCert(t, "R10", "Let's Encrypt", true, rootCert, rootKey)
	_, leaf1Key, leaf1PEM := makeTestCert(t, "vpn.example.com", "", false, inter1, inter1Key)
	inter2, inter2Key, inter2PEM := makeTestCert(t, "R11", "Let's Encrypt", true, rootCert, rootKey)
	_, leaf2Key, leaf2PEM := makeTestCert(t, "vpn.example.com", "", false, inter2, inter2Key)
	_, _, stagingPEM := makeTestCert(t, "(STAGING) Faux Intermediate", "(STAGING) Let's Encrypt", true, rootCert, rootKey)
	_, _, stagingLeafPEM := makeTestCert(t, "vpn.example.com", "", false, nil, nil)
	_ = stagingLeafPEM

	var mu sync.Mutex
	currentOvpnCA := string(leaf1PEM)
	var serverCertMu sync.RWMutex
	var activeCert tls.Certificate
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		result := M{}
		switch str(req["method"]) {
		case "MakeOpenVpnConfigFile":
			body := "client\r\nremote auto.softether.net 1194\r\n<ca>\r\n" +
				strings.ReplaceAll(strings.TrimSpace(currentOvpnCA), "\n", "\r\n") +
				"\r\n</ca>\r\n"
			result["Buffer_bin"] = base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{
				"hostopenvpn_remote_access_l3.ovpn": body,
			}))
		case "SetServerCert":
			rawCert, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Cert_bin"]))
			rawKey, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Key_bin"]))
			if pair, err := tls.X509KeyPair(rawCert, rawKey); err == nil {
				serverCertMu.Lock()
				activeCert = pair
				serverCertMu.Unlock()
			}
			certs := parsePEMCertificates(rawCert)
			if len(certs) > 0 {
				currentOvpnCA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0].Raw}))
			}
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		serverCertMu.RLock()
		c := activeCert
		serverCertMu.RUnlock()
		return &tls.Config{Certificates: []tls.Certificate{c}}, nil
	}}
	server.StartTLS()
	defer server.Close()
	serverCertMu.Lock()
	activeCert = server.TLS.Certificates[0]
	serverCertMu.Unlock()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	// Save managed Let's Encrypt production cert #1.
	key1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leaf1Key)})
	chain1 := append(append([]byte{}, leaf1PEM...), inter1PEM...)
	check(saveCertificate(a.tls.certDir("vpn.example.com"), chain1, key1PEM, M{"domain": "vpn.example.com", "staging": false}))
	a.set("domain", "vpn.example.com")
	a.set("cert_auto_push", true)
	a.set("cert_push_server_id", 1)

	// 1. Download .ovpn: syncs inter1 into chain_certs and replaces <ca> with ISRG Root X1 + X2.
	status, out, _ := call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 {
		t.Fatalf("sample-config failed: %d %v", status, out)
	}
	ovpn1 := str(out["config"])
	if !strings.Contains(ovpn1, "\r\n<ca>\r\n") {
		t.Fatalf("expected CRLF preservation in .ovpn")
	}
	_, _, ca1Body, _, _, ok := extractOpenVPNCABlock(ovpn1)
	if !ok {
		t.Fatal("missing <ca> block")
	}
	ca1Certs := parsePEMCertificates([]byte(ca1Body))
	if len(ca1Certs) != 2 || ca1Certs[0].Subject.CommonName != "ISRG Root X1" || ca1Certs[1].Subject.CommonName != "ISRG Root X2" {
		t.Fatalf("expected ISRG Root X1 + X2 in <ca>, got %d certs", len(ca1Certs))
	}
	bChain, err := os.ReadFile(filepath.Join(chainDir, "sem-chain-1.cer"))
	if err != nil || !bytes.Equal(bChain, inter1PEM) {
		t.Fatalf("expected sem-chain-1.cer to contain inter1, got err=%v", err)
	}

	// 2. Simulate renewal to cert #2 signed by R11 and auto-push to SoftEther.
	key2PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leaf2Key)})
	chain2 := append(append([]byte{}, leaf2PEM...), inter2PEM...)
	check(saveCertificate(a.tls.certDir("vpn.example.com"), chain2, key2PEM, M{"domain": "vpn.example.com", "staging": false}))
	a.tls.deployCertificate(t.Context(), "vpn.example.com")

	bChain2, err := os.ReadFile(filepath.Join(chainDir, "sem-chain-1.cer"))
	if err != nil || !bytes.Equal(bChain2, inter2PEM) {
		t.Fatalf("expected sem-chain-1.cer updated to inter2 after renewal, got err=%v", err)
	}
	status, out, _ = call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 {
		t.Fatalf("sample-config after renewal failed: %d %v", status, out)
	}
	if str(out["config"]) != ovpn1 {
		t.Fatalf("expected .ovpn to remain identical across certificate renewals")
	}

	// 3. Remote server fallback (no chain_certs access): includes current intermediate alongside ISRG roots.
	a.cfg.SEChainCertsDir = ""
	a.cfg.ChainCertSearchDirs = nil
	status, out, _ = call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 {
		t.Fatalf("remote sample-config failed: %d %v", status, out)
	}
	_, _, caRemoteBody, _, _, _ := extractOpenVPNCABlock(str(out["config"]))
	caRemoteCerts := parsePEMCertificates([]byte(caRemoteBody))
	if len(caRemoteCerts) != 3 || caRemoteCerts[2].Subject.CommonName != "R11" {
		t.Fatalf("expected ISRG Root X1 + X2 + R11 fallback for remote server, got %d certs", len(caRemoteCerts))
	}

	// 4. Self-signed and staging certificates must NOT be rewritten.
	mu.Lock()
	currentOvpnCA = string(rootPEM)
	mu.Unlock()
	status, out, _ = call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 || !strings.Contains(strings.ReplaceAll(str(out["config"]), "\r\n", "\n"), strings.TrimSpace(string(rootPEM))) {
		t.Fatalf("expected self-signed cert in <ca> to remain untouched")
	}

	mu.Lock()
	currentOvpnCA = string(stagingPEM)
	mu.Unlock()
	status, out, _ = call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 || !strings.Contains(strings.ReplaceAll(str(out["config"]), "\r\n", "\n"), strings.TrimSpace(string(stagingPEM))) {
		t.Fatalf("expected staging cert in <ca> to remain untouched")
	}
}

func TestRemoteChainHookReceivesChainDirAndSyncMarker(t *testing.T) {
	a := testApp(t)
	a.cfg.Root = t.TempDir()
	token := admin(t, a)

	// Verify resolveCertHookPath prefers an existing hook inside DataDir/ssl when Root != DataDir.
	dataHook := filepath.Join(a.cfg.DataDir, "ssl", "hook.sh")
	if err := atomicWrite(dataHook, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveCertHookPath(a.cfg.Root, a.cfg.DataDir, "ssl/hook.sh")
	if err != nil || resolved != dataHook {
		t.Fatalf("expected DataDir ssl/hook.sh %q, got %q (err=%v)", dataHook, resolved, err)
	}
	_ = os.Remove(dataHook)

	rootCert, rootKey, _ := makeTestCert(t, "Test Root", "ISRG", true, nil, nil)
	inter1, inter1Key, inter1PEM := makeTestCert(t, "YR1", "Let's Encrypt", true, rootCert, rootKey)
	_, leaf1Key, leaf1PEM := makeTestCert(t, "vpn.example.com", "", false, inter1, inter1Key)

	var certMu sync.RWMutex
	var activeCert tls.Certificate
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := M{}
		switch str(req["method"]) {
		case "MakeOpenVpnConfigFile":
			body := "client\nremote auto.softether.net 1194\n<ca>\n" + string(leaf1PEM) + "</ca>\n"
			result["Buffer_bin"] = base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{
				"hostopenvpn_remote_access_l3.ovpn": body,
			}))
		case "SetServerCert":
			rawCert, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Cert_bin"]))
			rawKey, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Key_bin"]))
			if pair, err := tls.X509KeyPair(rawCert, rawKey); err == nil {
				certMu.Lock()
				activeCert = pair
				certMu.Unlock()
			}
		default:
			t.Errorf("unexpected RPC: %v", req["method"])
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		certMu.RLock()
		c := activeCert
		certMu.RUnlock()
		return &tls.Config{Certificates: []tls.Certificate{c}}, nil
	}}
	server.StartTLS()
	defer server.Close()
	certMu.Lock()
	activeCert = server.TLS.Certificates[0]
	certMu.Unlock()
	target, _ := url.Parse(server.URL)
	host, port, _ := strings.Cut(target.Host, ":")
	a.saveConnection(host, int(integer(port)), ptr("vpn-secret"), "header")

	key1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leaf1Key)})
	chain1 := append(append([]byte{}, leaf1PEM...), inter1PEM...)
	check(saveCertificate(a.tls.certDir("vpn.example.com"), chain1, key1PEM, M{"domain": "vpn.example.com", "staging": false}))
	a.set("domain", "vpn.example.com")

	envCapture := filepath.Join(a.cfg.DataDir, "hook-env.txt")
	hookName := "hook.sh"
	hookScript := "#!/bin/sh\nprintf '%s|%s|%s' \"$CERT_DOMAIN\" \"$SE_SERVER_HOST\" \"$CERT_CHAIN_DIR\" > '" + envCapture + "'\n: > \"$CERT_CHAIN_DIR/.synced\"\n"
	if runtime.GOOS == "windows" {
		hookName = "hook.cmd"
		hookScript = "@echo off\r\n> \"" + envCapture + "\" echo %CERT_DOMAIN%^|%SE_SERVER_HOST%^|%CERT_CHAIN_DIR%\r\ntype nul > \"%CERT_CHAIN_DIR%\\.synced\"\r\n"
	}
	if err := atomicWrite(filepath.Join(a.cfg.DataDir, "ssl", hookName), []byte(hookScript), 0755); err != nil {
		t.Fatal(err)
	}
	a.set("cert_renew_hook", "ssl/"+hookName)

	if err := a.tls.pushDomainCertificate(t.Context(), "vpn.example.com"); err != nil {
		t.Fatalf("pushDomainCertificate failed: %v", err)
	}
	rawCaptured, err := os.ReadFile(envCapture)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSpace(string(rawCaptured)), "|")
	expectedChainDir := filepath.Join(a.cfg.DataDir, "ssl", "chain", "vpn.example.com")
	if len(parts) != 3 || parts[0] != "vpn.example.com" || parts[1] != host || parts[2] != expectedChainDir {
		t.Fatalf("unexpected hook env: %q", string(rawCaptured))
	}
	bCer, err := os.ReadFile(filepath.Join(expectedChainDir, "sem-chain-1.cer"))
	if err != nil || !bytes.Equal(bCer, inter1PEM) {
		t.Fatalf("expected pre-split sem-chain-1.cer in CERT_CHAIN_DIR, err=%v", err)
	}

	// With .synced present from hook, remote .ovpn omits current intermediate and uses ISRG Root X1 + X2 only.
	status, out, _ := call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 {
		t.Fatalf("sample-config failed: %d %v", status, out)
	}
	_, _, caBody, _, _, ok := extractOpenVPNCABlock(str(out["config"]))
	if !ok {
		t.Fatal("missing <ca>")
	}
	caCerts := parsePEMCertificates([]byte(caBody))
	if len(caCerts) != 2 || caCerts[0].Subject.CommonName != "ISRG Root X1" || caCerts[1].Subject.CommonName != "ISRG Root X2" {
		t.Fatalf("expected pure ISRG Root X1 + X2 when remote hook synced chain, got %d certs", len(caCerts))
	}
}
