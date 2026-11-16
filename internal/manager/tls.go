package manager

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"golang.org/x/crypto/acme"
	"golang.org/x/net/idna"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type tlsManager struct {
	app                               *App
	mu                                sync.Mutex
	http, https                       *http.Server
	httpsPort                         int
	certSerial                        string
	httpError, httpsError, httpsSince string
	tokens                            map[string]string
	busy, requested                   bool
	phase, step                       string
	dns, state                        M
	events                            []M
	nextAttempt                       time.Time
	lastConfig                        string
}

func newTLSManager(a *App) *tlsManager {
	m := &tlsManager{app: a, tokens: map[string]string{}, phase: "idle", state: M{}, dns: M{}, events: []M{}}
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "tls", "state.json"))
	if err == nil {
		_ = json.Unmarshal(b, &m.state)
	}
	if m.state == nil {
		m.state = M{}
	}
	m.nextAttempt = parseTime(str(m.state["next_attempt_at"]))
	return m
}
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".sem-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func normalizeDomain(value string) string {
	name := strings.ToLower(strings.Trim(strings.TrimSpace(value), "."))
	if name == "" {
		return ""
	}
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil || len(ascii) > 253 || net.ParseIP(ascii) != nil {
		fail(422, "A valid public domain name is required.")
	}
	labels := strings.Split(ascii, ".")
	valid := regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	if len(labels) < 2 {
		fail(422, "A public domain name needs at least two parts.")
	}
	for _, label := range labels {
		if !valid.MatchString(label) {
			fail(422, "Invalid hostname label: "+label)
		}
	}
	if regexp.MustCompile(`^\d+$`).MatchString(labels[len(labels)-1]) {
		fail(422, "A domain name cannot end in a number.")
	}
	return ascii
}
func (m *tlsManager) certDir(domain string) string {
	return filepath.Join(m.app.cfg.DataDir, "tls", "certs", domain)
}
func (m *tlsManager) certificate(domain string) (M, *tls.Certificate) {
	out := M{"present": false, "domain": "", "issued_at": "", "not_before": "", "expires_at": "", "days_left": nil, "renew_at": "", "issuer": "", "serial": "", "staging": false, "expired": false, "stale": domain != "", "matches_domain": false}
	if domain == "" {
		return out, nil
	}
	dir := m.certDir(domain)
	meta := M{}
	var cert tls.Certificate
	bundle, err := os.ReadFile(filepath.Join(dir, "bundle.pem"))
	if os.IsNotExist(err) {
		cert, err = tls.LoadX509KeyPair(filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem"))
		if b, e := os.ReadFile(filepath.Join(dir, "meta.json")); e == nil {
			_ = json.Unmarshal(b, &meta)
		}
	} else if err == nil {
		cert, err = tls.X509KeyPair(bundle, bundle)
		for rest := bundle; len(rest) > 0; {
			block, remaining := pem.Decode(rest)
			if block == nil {
				break
			}
			if block.Type == "SEM METADATA" {
				_ = json.Unmarshal(block.Bytes, &meta)
			}
			rest = remaining
		}
	}
	if err != nil || len(cert.Certificate) == 0 {
		return out, nil
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return out, nil
	}
	cert.Leaf = leaf
	matches := leaf.VerifyHostname(domain) == nil
	staging := boolean(meta["staging"])
	out = M{"present": true, "domain": domain, "issued_at": str(meta["issued_at"]), "not_before": leaf.NotBefore.UTC().Format(time.RFC3339), "expires_at": leaf.NotAfter.UTC().Format(time.RFC3339), "days_left": int(time.Until(leaf.NotAfter).Hours() / 24), "renew_at": leaf.NotAfter.Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339), "issuer": leaf.Issuer.String(), "serial": leaf.SerialNumber.String(), "staging": staging, "expired": time.Now().After(leaf.NotAfter), "stale": !matches || time.Now().After(leaf.NotAfter) || time.Now().Before(leaf.NotBefore) || staging != boolean(m.app.setting("acme_staging")), "matches_domain": matches}
	return out, &cert
}
func (m *tlsManager) challenge(w http.ResponseWriter, r *http.Request) bool {
	prefix := "/.well-known/acme-challenge/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	m.mu.Lock()
	response, ok := m.tokens[strings.TrimPrefix(r.URL.Path, prefix)]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(response))
	return true
}
func (m *tlsManager) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.http != nil {
		_ = m.http.Close()
		m.http = nil
	}
	if m.https != nil {
		_ = m.https.Close()
		m.https = nil
	}
}
func (m *tlsManager) apply() {
	a := m.app
	domain := str(a.setting("domain"))
	port := int(integer(a.setting("https_port")))
	certInfo, cert := m.certificate(domain)
	m.mu.Lock()
	defer m.mu.Unlock()
	if domain == "" {
		if m.http != nil {
			m.http.Close()
			m.http = nil
		}
		if m.https != nil {
			m.https.Close()
			m.https = nil
		}
		m.phase = "idle"
		return
	}
	if m.http == nil {
		server := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.challenge(w, r) {
				return
			}
			if !a.hostAllowed(r) {
				writeJSON(w, 404, M{"detail": "Not found."})
				return
			}
			info, _ := m.certificate(str(a.setting("domain")))
			if boolean(info["stale"]) {
				writeJSON(w, 503, M{"detail": "Waiting for a valid HTTPS certificate."})
				return
			}
			origin := m.origin()
			http.Redirect(w, r, origin+r.URL.RequestURI(), http.StatusTemporaryRedirect)
		})}
		listener, err := net.Listen("tcp", net.JoinHostPort(a.cfg.BindHost, "80"))
		if err != nil {
			m.httpError = err.Error()
		} else {
			m.http = server
			m.httpError = ""
			go func() {
				err := server.Serve(listener)
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.http == server {
					m.http = nil
					if err != http.ErrServerClosed {
						m.httpError = str(err)
					}
				}
			}()
		}
	}
	serial := str(certInfo["serial"])
	valid := cert != nil && !boolean(certInfo["stale"])
	if m.https != nil && (m.httpsPort != port || m.certSerial != serial || !valid) {
		m.https.Close()
		m.https = nil
	}
	if valid && m.https == nil {
		server := &http.Server{ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, Handler: a.Handler(), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*cert}}}
		listener, err := net.Listen("tcp", net.JoinHostPort(a.cfg.BindHost, fmt.Sprint(port)))
		if err != nil {
			m.httpsError = err.Error()
		} else {
			m.https, m.httpsPort, m.certSerial = server, port, serial
			m.httpsError = ""
			m.httpsSince = now()
			go func() {
				err := server.Serve(tls.NewListener(listener, server.TLSConfig))
				m.mu.Lock()
				defer m.mu.Unlock()
				if m.https == server {
					m.https = nil
					if err != http.ErrServerClosed {
						m.httpsError = str(err)
					}
				}
			}()
		}
	}
	if !m.busy {
		if valid {
			m.phase = "ok"
		} else if str(m.state["last_error"]) != "" {
			m.phase = "failed"
		} else {
			m.phase = "waiting"
		}
	}
}
func (m *tlsManager) origin() string {
	domain := str(m.app.setting("domain"))
	port := integer(m.app.setting("https_port"))
	if port != 443 {
		domain = net.JoinHostPort(domain, fmt.Sprint(port))
	}
	return "https://" + domain
}
func (m *tlsManager) checkDNS(ctx context.Context, domain string) M {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupHost(ctx, domain)
	local := []string{}
	if values, e := net.InterfaceAddrs(); e == nil {
		for _, address := range values {
			ip, _, _ := net.ParseCIDR(address.String())
			if ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				local = append(local, ip.String())
			}
		}
	}
	var match any
	if len(addresses) > 0 {
		match = false
		for _, ip := range addresses {
			for _, own := range local {
				if ip == own {
					match = true
				}
			}
		}
	}
	out := M{"domain": domain, "checked_at": now(), "addresses": addresses, "local_addresses": local, "match": match, "error": ""}
	if err != nil {
		out["error"] = err.Error()
	}
	m.mu.Lock()
	m.dns = out
	m.mu.Unlock()
	return out
}
func (m *tlsManager) persistLocked() {
	b, err := json.Marshal(m.state)
	if err == nil {
		err = atomicWrite(filepath.Join(m.app.cfg.DataDir, "tls", "state.json"), b, 0600)
	}
	if err != nil {
		logTLS(err)
	}
}
func logTLS(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "TLS: %v\n", err)
	}
}
func (m *tlsManager) start(ctx context.Context) {
	m.app.worker(ctx, time.Millisecond, func() time.Duration { return 30 * time.Second }, func(ctx context.Context) {
		m.apply()
		domain := str(m.app.setting("domain"))
		if domain == "" {
			return
		}
		cert, _ := m.certificate(domain)
		m.mu.Lock()
		requested := m.requested
		m.requested = false
		canRetry := time.Now().After(m.nextAttempt)
		m.mu.Unlock()
		due := boolean(cert["stale"]) || !parseTime(str(cert["renew_at"])).After(time.Now())
		if requested || (due && canRetry) {
			m.issue(ctx, domain)
		}
	})
}
func newCertificateOrder(ctx context.Context, client *acme.Client, account *acme.Account, domain string) (*acme.Order, error) {
	if _, err := client.Register(ctx, account, acme.AcceptTOS); err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, err
	}
	return client.AuthorizeOrder(ctx, acme.DomainIDs(domain))
}

func (m *tlsManager) issue(ctx context.Context, domain string) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return
	}
	m.busy = true
	m.phase = "issuing"
	m.step = "checking the DNS record"
	m.state["last_attempt_at"], m.state["last_attempt_kind"] = now(), "issuance"
	m.persistLocked()
	m.mu.Unlock()
	var issueErr error
	defer func() {
		if p := recover(); p != nil {
			issueErr = fmt.Errorf("%v", p)
		}
		m.mu.Lock()
		m.busy = false
		m.step = ""
		if issueErr != nil {
			m.phase = "failed"
			m.state["last_error"], m.state["last_error_at"], m.state["last_error_type"] = issueErr.Error(), now(), "acme"
			failures := integer(m.state["consecutive_failures"]) + 1
			m.state["consecutive_failures"] = failures
			m.nextAttempt = time.Now().Add(time.Duration(min(int64(3600), 60*(1<<min(failures, int64(6))))) * time.Second)
			m.state["next_attempt_at"] = m.nextAttempt.UTC().Format(time.RFC3339)
		} else {
			m.phase = "ok"
			m.state["last_error"], m.state["last_error_type"], m.state["last_success_at"], m.state["consecutive_failures"], m.state["next_attempt_at"] = "", "", now(), 0, ""
		}
		m.events = append(m.events, M{"at": now(), "kind": m.phase, "message": str(m.state["last_error"])})
		if len(m.events) > 30 {
			m.events = m.events[len(m.events)-30:]
		}
		m.persistLocked()
		m.mu.Unlock()
		m.apply()
	}()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	dns := m.checkDNS(ctx, domain)
	if str(dns["error"]) != "" {
		check(fmt.Errorf("DNS: %s", dns["error"]))
	}
	m.mu.Lock()
	httpReady := m.http != nil
	m.mu.Unlock()
	if !httpReady {
		check(fmt.Errorf("port 80 is unavailable for certificate validation"))
	}
	staging := boolean(m.app.setting("acme_staging"))
	base := filepath.Join(m.app.cfg.DataDir, "tls")
	accountFile := "account.key"
	directory := acme.LetsEncryptURL
	if staging {
		accountFile = "account-staging.key"
		directory = "https://acme-staging-v02.api.letsencrypt.org/directory"
	}
	key, err := loadOrCreateKey(filepath.Join(base, accountFile))
	check(err)
	client := &acme.Client{Key: key, DirectoryURL: directory, HTTPClient: &http.Client{Timeout: 30 * time.Second}, UserAgent: "SoftEther-Manager/" + m.app.cfg.Version}
	account := &acme.Account{}
	if email := str(m.app.setting("acme_email")); email != "" {
		account.Contact = []string{"mailto:" + email}
	}
	order, err := newCertificateOrder(ctx, client, account, domain)
	check(err)
	for _, uri := range order.AuthzURLs {
		auth, err := client.GetAuthorization(ctx, uri)
		check(err)
		if auth.Status == acme.StatusValid {
			continue
		}
		var challenge *acme.Challenge
		for _, c := range auth.Challenges {
			if c.Type == "http-01" {
				challenge = c
				break
			}
		}
		if challenge == nil {
			check(fmt.Errorf("ACME server offered no HTTP-01 challenge"))
		}
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		check(err)
		m.mu.Lock()
		m.tokens[challenge.Token] = response
		m.step = "waiting for Let's Encrypt to validate the domain over port 80"
		m.mu.Unlock()
		defer func(token string) { m.mu.Lock(); delete(m.tokens, token); m.mu.Unlock() }(challenge.Token)
		_, err = client.Accept(ctx, challenge)
		check(err)
		_, err = client.WaitAuthorization(ctx, uri)
		check(err)
	}
	order, err = client.WaitOrder(ctx, order.URI)
	check(err)
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: domain}, DNSNames: []string{domain}}, certKey)
	check(err)
	der, _, err := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	check(err)
	chain := []byte{}
	for _, cert := range der {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	check(err)
	check(saveCertificate(m.certDir(domain), chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), M{"domain": domain, "staging": staging, "issued_at": now(), "directory": directory}))
}

func saveCertificate(dir string, chain, key []byte, meta M) error {
	if _, err := tls.X509KeyPair(chain, key); err != nil {
		return err
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	bundle := append(append(append([]byte{}, chain...), key...), pem.EncodeToMemory(&pem.Block{Type: "SEM METADATA", Bytes: encoded})...)
	return atomicWrite(filepath.Join(dir, "bundle.pem"), bundle, 0600)
}
func loadOrCreateKey(path string) (crypto.Signer, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("invalid account key")
		}
		if key, e := x509.ParsePKCS8PrivateKey(block.Bytes); e == nil {
			if signer, ok := key.(crypto.Signer); ok {
				return signer, nil
			}
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return key, atomicWrite(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600)
}
func (m *tlsManager) status(r *http.Request) M {
	a := m.app
	domain := str(a.setting("domain"))
	cert, _ := m.certificate(domain)
	m.mu.Lock()
	defer m.mu.Unlock()
	renewal := clone(m.state)
	for _, key := range []string{"last_attempt_at", "last_attempt_kind", "last_success_at", "last_error", "last_error_type", "last_error_at", "next_attempt_at"} {
		if renewal[key] == nil {
			renewal[key] = ""
		}
	}
	renewal["automatic"], renewal["renew_before_days"], renewal["renew_at"], renewal["due"] = true, 30, cert["renew_at"], boolean(cert["present"]) && !parseTime(str(cert["renew_at"])).After(time.Now())
	renewal["consecutive_failures"] = integer(renewal["consecutive_failures"])
	origin, url := "", ""
	if domain != "" {
		origin = m.origin()
		url = origin + "/"
		if path := strings.Trim(str(a.setting("web_path")), "/"); path != "" {
			url += path + "/"
		}
	}
	return M{"configured": domain != "", "domain": domain, "https_port": a.setting("https_port"), "acme_email": a.setting("acme_email"), "acme_staging": a.setting("acme_staging"), "domain_only": a.setting("domain_only"), "config_error": "", "phase": m.phase, "busy": m.busy, "step": m.step, "dns": clone(m.dns), "http": M{"port": 80, "listening": m.http != nil, "error": m.httpError, "redirecting": m.http != nil && m.https != nil}, "https": M{"port": a.setting("https_port"), "listening": m.https != nil, "error": m.httpsError, "since": m.httpsSince}, "certificate": cert, "renewal": renewal, "url": url, "origin": origin, "events": append([]M{}, m.events...), "request_host": hostName(r.Host), "via_domain": domain != "" && hostName(r.Host) == domain, "via_loopback": loopback(r), "bind_port": a.cfg.BindPort, "web_path": a.setting("web_path")}
}
