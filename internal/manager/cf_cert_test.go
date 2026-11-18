package manager

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveToSSLDir(t *testing.T) {
	root := t.TempDir()
	data := t.TempDir()
	domain := "vpn.example.com"

	// Generate a dummy RSA 2048 cert and key
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: domain,
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(24 * time.Hour),
		DNSNames:  []string{domain},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}

	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	err = saveToSSLDir(root, data, domain, chain, keyPEM)
	if err != nil {
		t.Fatalf("saveToSSLDir failed: %v", err)
	}

	sslDir := filepath.Join(root, "ssl")
	pemPath := filepath.Join(sslDir, domain+".pem")
	keyPath := filepath.Join(sslDir, domain+".key")
	bundlePath := filepath.Join(sslDir, domain+".pem.key")

	if _, err := os.Stat(pemPath); err != nil {
		t.Errorf("expected %s to exist", pemPath)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Errorf("expected %s to exist", keyPath)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Errorf("expected %s to exist", bundlePath)
	}

	b, _ := os.ReadFile(pemPath)
	if string(b) != string(chain) {
		t.Errorf("pem content mismatch")
	}
}

func TestPublicServerHostSetting(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// Set public host and port via /system/vpn-template
	status, out, _ := call(t, a, "PUT", "/api/v1/system/vpn-template", M{
		"vpn_public_host": "vpn.example.com",
		"vpn_public_port": 443,
	}, token)
	if status != 200 {
		t.Fatalf("expected 200, got %d: %v", status, out)
	}
	if str(out["vpn_public_host"]) != "vpn.example.com" {
		t.Errorf("expected vpn.example.com, got %v", out["vpn_public_host"])
	}
	if integer(out["vpn_public_port"]) != 443 {
		t.Errorf("expected 443, got %v", out["vpn_public_port"])
	}

	// Verify it keeps panel HTTPS domain empty and decoupled from vpn_public_host
	domStatus, domOut, _ := call(t, a, "GET", "/api/v1/system/domain", nil, token)
	if domStatus != 200 {
		t.Fatalf("expected 200, got %d", domStatus)
	}
	if str(domOut["domain"]) != "" {
		t.Errorf("expected empty panel domain, got %v", domOut["domain"])
	}
	if str(domOut["vpn_public_host"]) != "vpn.example.com" {
		t.Errorf("expected vpn_public_host vpn.example.com, got %v", domOut["vpn_public_host"])
	}
}
