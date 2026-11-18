package manager

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// SetServerCert swaps the certificate the panel itself pins; the panel must move
// its own pin, otherwise the next connection looks like a MITM and locks it out.
func TestPushingCertificateRepinsServerTrust(t *testing.T) {
	a := testApp(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": M{}})
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, portText, _ := strings.Cut(u.Host, ":")
	port := int(integer(portText))
	a.saveConnection(host, port, ptr("secret"), "header")
	if _, err := a.client().Call(context.Background(), "GetServerInfo", nil); err != nil {
		t.Fatal(err)
	}
	oldPin := str(one(a.db, `SELECT Fingerprint FROM RPCTrust WHERE Host=? AND Port=?`, host, port)["Fingerprint"])

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"vpn.example.com"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	check(err)
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	check(err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := a.tls.pushCertToServer(context.Background(), "vpn.example.com", chain, keyPEM); err != nil {
		t.Fatal(err)
	}
	want := certFingerprint(raw)
	if want == oldPin {
		t.Fatal("test certificate matches the original pin")
	}
	if got := str(one(a.db, `SELECT Fingerprint FROM RPCTrust WHERE Host=? AND Port=?`, host, port)["Fingerprint"]); got != want {
		t.Fatalf("pin not moved to the pushed certificate: got %s want %s", got, want)
	}
	a.connectionMu.Lock()
	cached := a.rpcClients[a.serverIDFromContext(context.Background())]
	a.connectionMu.Unlock()
	if cached != nil {
		t.Fatal("cached client kept the stale handshake")
	}
}

func TestPushRejectsCertificateWithoutItsKey(t *testing.T) {
	a := testApp(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(11), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"vpn.example.com"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	check(err)
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	otherDER, err := x509.MarshalPKCS8PrivateKey(other)
	check(err)
	otherPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: otherDER})

	if err := a.tls.pushCertToServer(context.Background(), "vpn.example.com", chain, otherPEM); err == nil {
		t.Fatal("pushed a certificate whose key does not match")
	}
	if err := a.tls.pushCertToServer(context.Background(), "vpn.example.com", chain, chain); err == nil {
		t.Fatal("pushed a certificate as its own key")
	}
}

func TestFingerprintOfPEMMatchesObservedCertificate(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	check(err)
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})...)
	if got, want := fingerprintOfPEM(chain), certFingerprint(raw); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if fingerprintOfPEM([]byte("not a certificate")) != "" {
		t.Fatal("garbage produced a fingerprint")
	}
}

func TestInspectCertificateSurfacesConnectionError(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	u, _ := url.Parse(server.URL)
	host, portText, _ := strings.Cut(u.Host, ":")
	port := int(integer(portText))
	server.Close()
	a.saveConnection(host, port, ptr("secret"), "header")

	status, out, _ := call(t, a, "GET", "/api/v1/servers/1/certificate", nil, token)
	if status != 502 || !strings.Contains(str(out["detail"]), "Cannot inspect SoftEther certificate:") {
		t.Fatalf("expected 502 with transport detail, got %d %v", status, out)
	}
}
