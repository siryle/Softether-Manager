package manager

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const rpcTrustTimeout = 10 * time.Second

func certFingerprint(raw []byte) string {
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// fingerprintOfPEM predicts the pin the panel will observe for the leaf of a PEM chain.
func fingerprintOfPEM(chain []byte) string {
	for block, rest := pem.Decode(chain); block != nil; block, rest = pem.Decode(rest) {
		if block.Type == "CERTIFICATE" {
			return certFingerprint(block.Bytes)
		}
	}
	return ""
}

func (a *App) newRPCClient(host string, port int, password, auth string, skipCertCheck bool) *RPCClient {
	c := NewRPCClient(host, port, password, auth)
	transport := c.http.Transport.(*http.Transport)
	if skipCertCheck {
		// Explicitly opted out per server: the administrator password is sent to
		// whatever answers on this address. Audited when the flag is turned on.
		transport.TLSClientConfig.InsecureSkipVerify = true
		return c
	}
	// Self-signed SoftEther certificates are checked against a durable first-use pin.
	transport.TLSClientConfig.InsecureSkipVerify = true
	transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return fmt.Errorf("SoftEther supplied no certificate")
		}
		fingerprint := certFingerprint(state.PeerCertificates[0].Raw)
		if _, err := a.db.Exec(`INSERT INTO RPCTrust(Host,Port,Fingerprint) VALUES(?,?,?) ON CONFLICT DO NOTHING`, host, port, fingerprint); err != nil {
			return err
		}
		var pinned string
		if err := a.db.QueryRow(`SELECT Fingerprint FROM RPCTrust WHERE Host=? AND Port=?`, host, port).Scan(&pinned); err != nil {
			return err
		}
		if pinned != fingerprint {
			return fmt.Errorf("SoftEther certificate changed (SHA-256 %s); verify it on the server and confirm it in Servers", fingerprint)
		}
		return nil
	}
	return c
}

// This handshake sends no HTTP request or administrator credential.
func remoteFingerprint(ctx context.Context, host string, port int) (string, error) {
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: rpcTrustTimeout}, Config: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("SoftEther supplied no certificate")
	}
	return certFingerprint(state.PeerCertificates[0].Raw), nil
}

func (a *App) registerRPCTrust() {
	a.endpoint("GET /servers/{id}/certificate", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		row := a.serverRow(serverID(r))
		ctx, cancel := context.WithTimeout(r.Context(), rpcTrustTimeout)
		defer cancel()
		fingerprint, err := remoteFingerprint(ctx, str(row["Host"]), int(integer(row["Port"])))
		check(err)
		pin := one(a.db, `SELECT Fingerprint FROM RPCTrust WHERE Host=? AND Port=?`, row["Host"], row["Port"])
		return M{"fingerprint": fingerprint, "pinned": str(pin["Fingerprint"])}
	})
	a.endpoint("PUT /servers/{id}/certificate", func(w http.ResponseWriter, r *http.Request, user M) any {
		body := readBody(w, r)
		expected := strings.ToLower(str(body["fingerprint"]))
		if decoded, err := hex.DecodeString(expected); err != nil || len(decoded) != 32 {
			fail(422, "A SHA-256 certificate fingerprint is required.")
		}
		a.connectionMu.Lock()
		defer a.connectionMu.Unlock()
		row := a.serverRow(serverID(r))
		ctx, cancel := context.WithTimeout(r.Context(), rpcTrustTimeout)
		defer cancel()
		fingerprint, err := remoteFingerprint(ctx, str(row["Host"]), int(integer(row["Port"])))
		check(err)
		if fingerprint != expected {
			fail(409, "The certificate changed again; inspect it before confirming.")
		}
		execute(a.db, `INSERT INTO RPCTrust(Host,Port,Fingerprint) VALUES(?,?,?) ON CONFLICT(Host,Port) DO UPDATE SET Fingerprint=excluded.Fingerprint`, row["Host"], row["Port"], fingerprint)
		a.invalidateClientLocked(integer(row["ServerID"]))
		a.audit(user, "server.certificate_trusted", "server", str(row["Name"]), fingerprint, integer(row["ServerID"]))
		return M{"ok": true}
	})
}
