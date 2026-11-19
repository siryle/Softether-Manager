package manager

import (
	"archive/tar"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func startMockSSHServer(t *testing.T, expectedPubKey ssh.PublicKey, expectedPass string) (host string, port int, captured *[]string, files *map[string][]byte, rotateHostKey func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	cmds := []string{}
	uploaded := map[string][]byte{}
	captured = &cmds
	files = &uploaded

	makeSigner := func() ssh.Signer {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ssh.NewSignerFromKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	var hostKeyMu sync.RWMutex
	hostSigner := makeSigner()
	rotateHostKey = func() {
		hostKeyMu.Lock()
		hostSigner = makeSigner()
		hostKeyMu.Unlock()
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				cfg := &ssh.ServerConfig{
					PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
						if expectedPubKey != nil && bytes.Equal(key.Marshal(), expectedPubKey.Marshal()) {
							return &ssh.Permissions{}, nil
						}
						return nil, io.EOF
					},
					PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
						if expectedPass != "" && string(pass) == expectedPass {
							return &ssh.Permissions{}, nil
						}
						return nil, io.EOF
					},
				}
				hostKeyMu.RLock()
				cfg.AddHostKey(hostSigner)
				hostKeyMu.RUnlock()

				sConn, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sConn.Close()
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					if newChan.ChannelType() != "session" {
						_ = newChan.Reject(ssh.UnknownChannelType, "unknown")
						continue
					}
					ch, chReqs, err := newChan.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer ch.Close()
						for req := range chReqs {
							if req.Type != "exec" {
								if req.WantReply {
									_ = req.Reply(false, nil)
								}
								continue
							}
							var payload struct{ Cmd string }
							_ = ssh.Unmarshal(req.Payload, &payload)
							if req.WantReply {
								_ = req.Reply(true, nil)
							}
							mu.Lock()
							cmds = append(cmds, payload.Cmd)
							mu.Unlock()
							if strings.Contains(payload.Cmd, "tar -C") {
								tr := tar.NewReader(ch)
								for {
									hdr, err := tr.Next()
									if err == io.EOF {
										break
									}
									if err != nil {
										break
									}
									data, _ := io.ReadAll(tr)
									mu.Lock()
									uploaded[hdr.Name] = data
									mu.Unlock()
								}
							}
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							return
						}
					}()
				}
			}(conn)
		}
	}()

	h, pStr, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(pStr)
	return h, p, captured, files, rotateHostKey
}

func TestWebUISSHKeygenTestAndRemoteChainSync(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// 1. Generate SSH key via Web API; private key stays encrypted & redacted.
	status, out, _ := call(t, a, "POST", "/api/v1/system/domain/ssh-keygen", nil, token)
	if status != 200 || !boolean(out["has_cert_ssh_key"]) || !strings.HasPrefix(str(out["public_key"]), "ssh-ed25519 ") {
		t.Fatalf("ssh-keygen failed: %d %v", status, out)
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(str(out["public_key"])))
	if err != nil {
		t.Fatal(err)
	}

	status, domOut, _ := call(t, a, "GET", "/api/v1/system/domain", nil, token)
	if status != 200 || str(domOut["cert_ssh_key"]) != "" || !boolean(domOut["has_cert_ssh_key"]) || str(domOut["cert_ssh_public_key"]) != str(out["public_key"]) {
		t.Fatalf("GET /system/domain exposed or dropped SSH key fields: %v", domOut)
	}
	status, sysOut, _ := call(t, a, "GET", "/api/v1/system/settings", nil, token)
	if status != 200 || str(sysOut["cert_ssh_key"]) != "" || !boolean(sysOut["has_cert_ssh_key"]) {
		t.Fatalf("GET /system/settings leaked cert_ssh_key: %v", sysOut)
	}
	if strings.Contains(str(a.setting("cert_ssh_key")), "PRIVATE KEY") {
		t.Fatal("cert_ssh_key was not encrypted at rest")
	}

	// Invalid private key must return 422.
	status, _, _ = call(t, a, "PUT", "/api/v1/system/domain", M{"cert_ssh_key": "not-a-valid-key"}, token)
	if status != 422 {
		t.Fatalf("expected 422 for invalid SSH private key, got %d", status)
	}

	// 2. Start mock SSH server and test connection + TOFU host key pinning.
	sshHost, sshPort, cmds, uploaded, rotateHostKey := startMockSSHServer(t, pubKey, "s3cr3t")
	status, testOut, _ := call(t, a, "POST", "/api/v1/system/domain/test-ssh", M{
		"cert_ssh_host":       sshHost,
		"cert_ssh_port":       sshPort,
		"cert_ssh_user":       "root",
		"cert_ssh_auth_type":  "key",
		"cert_ssh_remote_dir": "/var/lib/softether/chain_certs",
	}, token)
	if status != 200 || str(testOut["remote_dir"]) != "/var/lib/softether/chain_certs" || !strings.HasPrefix(str(testOut["fingerprint"]), "SHA256:") {
		t.Fatalf("test-ssh failed: %d %v", status, testOut)
	}

	// Host key rotation must be rejected unless reset_host_key=true.
	rotateHostKey()
	status, _, _ = call(t, a, "POST", "/api/v1/system/domain/test-ssh", M{
		"cert_ssh_host":       sshHost,
		"cert_ssh_port":       sshPort,
		"cert_ssh_user":       "root",
		"cert_ssh_auth_type":  "key",
		"cert_ssh_remote_dir": "/var/lib/softether/chain_certs",
	}, token)
	if status != 502 {
		t.Fatalf("expected 502 on SSH host key change, got %d", status)
	}
	status, _, _ = call(t, a, "POST", "/api/v1/system/domain/test-ssh", M{
		"cert_ssh_host":       sshHost,
		"cert_ssh_port":       sshPort,
		"cert_ssh_user":       "root",
		"cert_ssh_auth_type":  "key",
		"cert_ssh_remote_dir": "/var/lib/softether/chain_certs",
		"reset_host_key":      true,
	}, token)
	if status != 200 {
		t.Fatalf("expected 200 after reset_host_key=true, got %d", status)
	}

	// Password authentication also works and stays redacted.
	status, _, _ = call(t, a, "POST", "/api/v1/system/domain/test-ssh", M{
		"cert_ssh_host":       sshHost,
		"cert_ssh_port":       sshPort,
		"cert_ssh_user":       "root",
		"cert_ssh_auth_type":  "password",
		"cert_ssh_password":   "s3cr3t",
		"cert_ssh_remote_dir": "/var/lib/softether/chain_certs",
	}, token)
	if status != 200 {
		t.Fatalf("password test-ssh failed: %d", status)
	}

	// 3. Save domain with cert_ssh_sync enabled, push certificate, and verify SSH upload + .ovpn roots.
	rootCert, rootKey, _ := makeTestCert(t, "Test Root", "ISRG", true, nil, nil)
	inter1, inter1Key, inter1PEM := makeTestCert(t, "YR1", "Let's Encrypt", true, rootCert, rootKey)
	_, leaf1Key, leaf1PEM := makeTestCert(t, "vpn.example.com", "", false, inter1, inter1Key)

	var certMu sync.RWMutex
	var activeCert tls.Certificate
	seServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		result := M{}
		switch str(req["method"]) {
		case "SetServerCert":
			rawCert, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Cert_bin"]))
			rawKey, _ := base64.StdEncoding.DecodeString(str(object(req["params"])["Key_bin"]))
			if pair, err := tls.X509KeyPair(rawCert, rawKey); err == nil {
				certMu.Lock()
				activeCert = pair
				certMu.Unlock()
			}
		case "MakeOpenVpnConfigFile":
			body := "client\nremote auto.softether.net 1194\n<ca>\n" + string(leaf1PEM) + "</ca>\n"
			result["Buffer_bin"] = base64.StdEncoding.EncodeToString(zipOf(t, map[string]string{
				"hostopenvpn_remote_access_l3.ovpn": body,
			}))
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}))
	seServer.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		certMu.RLock()
		c := activeCert
		certMu.RUnlock()
		return &tls.Config{Certificates: []tls.Certificate{c}}, nil
	}}
	seServer.StartTLS()
	defer seServer.Close()
	certMu.Lock()
	activeCert = seServer.TLS.Certificates[0]
	certMu.Unlock()

	target, _ := url.Parse(seServer.URL)
	seHost, sePort, _ := strings.Cut(target.Host, ":")
	a.saveConnection(seHost, int(integer(sePort)), ptr("vpn-secret"), "header")

	key1PEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leaf1Key)})
	chain1 := append(append([]byte{}, leaf1PEM...), inter1PEM...)
	check(saveCertificate(a.tls.certDir("vpn.example.com"), chain1, key1PEM, M{"domain": "vpn.example.com", "staging": false}))

	status, _, _ = call(t, a, "PUT", "/api/v1/system/domain", M{
		"domain":              "vpn.example.com",
		"cert_ssh_sync":       true,
		"cert_ssh_host":       sshHost,
		"cert_ssh_port":       sshPort,
		"cert_ssh_user":       "root",
		"cert_ssh_auth_type":  "key",
		"cert_ssh_remote_dir": "/var/lib/softether/chain_certs",
	}, token)
	if status != 200 {
		t.Fatalf("PUT /system/domain failed: %d", status)
	}

	status, pushOut, _ := call(t, a, "POST", "/api/v1/system/domain/push-cert", nil, token)
	if status != 200 {
		t.Fatalf("push-cert with SSH sync failed: %d %v", status, pushOut)
	}
	if !bytes.Equal((*uploaded)["sem-chain-1.cer"], inter1PEM) {
		t.Fatalf("expected sem-chain-1.cer uploaded over SSH, got cmds=%v keys=%v", *cmds, len(*uploaded))
	}

	status, ovpnOut, _ := call(t, a, "POST", "/api/v1/server/openvpn/sample-config", M{"host": "vpn.example.com"}, token)
	if status != 200 {
		t.Fatalf("sample-config failed: %d %v", status, ovpnOut)
	}
	_, _, caBody, _, _, ok := extractOpenVPNCABlock(str(ovpnOut["config"]))
	if !ok {
		t.Fatal("missing <ca>")
	}
	caCerts := parsePEMCertificates([]byte(caBody))
	if len(caCerts) != 2 || caCerts[0].Subject.CommonName != "ISRG Root X1" || caCerts[1].Subject.CommonName != "ISRG Root X2" {
		t.Fatalf("expected pure ISRG Root X1 + X2 after Web UI SSH sync, got %d certs", len(caCerts))
	}
}
