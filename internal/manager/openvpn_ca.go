package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Official Let's Encrypt root CAs (ISRG Root X1 valid until 2035, ISRG Root X2 valid until 2040).
// Generation Y roots (ISRG Root YR / YE) are cross-signed by X1 / X2 so embedding these
// long-lived anchors keeps .ovpn profiles valid across leaf and intermediate renewals.
const isrgRootX1PEM = `-----BEGIN CERTIFICATE-----
MIIFazCCA1OgAwIBAgIRAIIQz7DSQONZRGPgu2OCiwAwDQYJKoZIhvcNAQELBQAw
TzELMAkGA1UEBhMCVVMxKTAnBgNVBAoTIEludGVybmV0IFNlY3VyaXR5IFJlc2Vh
cmNoIEdyb3VwMRUwEwYDVQQDEwxJU1JHIFJvb3QgWDEwHhcNMTUwNjA0MTEwNDM4
WhcNMzUwNjA0MTEwNDM4WjBPMQswCQYDVQQGEwJVUzEpMCcGA1UEChMgSW50ZXJu
ZXQgU2VjdXJpdHkgUmVzZWFyY2ggR3JvdXAxFTATBgNVBAMTDElTUkcgUm9vdCBY
MTCCAiIwDQYJKoZIhvcNAQEBBQADggIPADCCAgoCggIBAK3oJHP0FDfzm54rVygc
h77ct984kIxuPOZXoHj3dcKi/vVqbvYATyjb3miGbESTtrFj/RQSa78f0uoxmyF+
0TM8ukj13Xnfs7j/EvEhmkvBioZxaUpmZmyPfjxwv60pIgbz5MDmgK7iS4+3mX6U
A5/TR5d8mUgjU+g4rk8Kb4Mu0UlXjIB0ttov0DiNewNwIRt18jA8+o+u3dpjq+sW
T8KOEUt+zwvo/7V3LvSye0rgTBIlDHCNAymg4VMk7BPZ7hm/ELNKjD+Jo2FR3qyH
B5T0Y3HsLuJvW5iB4YlcNHlsdu87kGJ55tukmi8mxdAQ4Q7e2RCOFvu396j3x+UC
B5iPNgiV5+I3lg02dZ77DnKxHZu8A/lJBdiB3QW0KtZB6awBdpUKD9jf1b0SHzUv
KBds0pjBqAlkd25HN7rOrFleaJ1/ctaJxQZBKT5ZPt0m9STJEadao0xAH0ahmbWn
OlFuhjuefXKnEgV4We0+UXgVCwOPjdAvBbI+e0ocS3MFEvzG6uBQE3xDk3SzynTn
jh8BCNAw1FtxNrQHusEwMFxIt4I7mKZ9YIqioymCzLq9gwQbooMDQaHWBfEbwrbw
qHyGO0aoSCqI3Haadr8faqU9GY/rOPNk3sgrDQoo//fb4hVC1CLQJ13hef4Y53CI
rU7m2Ys6xt0nUW7/vGT1M0NPAgMBAAGjQjBAMA4GA1UdDwEB/wQEAwIBBjAPBgNV
HRMBAf8EBTADAQH/MB0GA1UdDgQWBBR5tFnme7bl5AFzgAiIyBpY9umbbjANBgkq
hkiG9w0BAQsFAAOCAgEAVR9YqbyyqFDQDLHYGmkgJykIrGF1XIpu+ILlaS/V9lZL
ubhzEFnTIZd+50xx+7LSYK05qAvqFyFWhfFQDlnrzuBZ6brJFe+GnY+EgPbk6ZGQ
3BebYhtF8GaV0nxvwuo77x/Py9auJ/GpsMiu/X1+mvoiBOv/2X/qkSsisRcOj/KK
NFtY2PwByVS5uCbMiogziUwthDyC3+6WVwW6LLv3xLfHTjuCvjHIInNzktHCgKQ5
ORAzI4JMPJ+GslWYHb4phowim57iaztXOoJwTdwJx4nLCgdNbOhdjsnvzqvHu7Ur
TkXWStAmzOVyyghqpZXjFaH3pO3JLF+l+/+sKAIuvtd7u+Nxe5AW0wdeRlN8NwdC
jNPElpzVmbUq4JUagEiuTDkHzsxHpFKVK7q4+63SM1N95R1NbdWhscdCb+ZAJzVc
oyi3B43njTOQ5yOf+1CceWxG1bQVs5ZufpsMljq4Ui0/1lvh+wjChP4kqKOJ2qxq
4RgqsahDYVvTH9w7jXbyLeiNdd8XM2w9U/t7y0Ff/9yi0GE44Za4rF2LN9d11TPA
mRGunUHBcnWEvgJBQl9nJEiU0Zsnvgc/ubhPgXRR4Xq37Z0j4r7g1SgEEzwxA57d
emyPxgcYxn/eR44/KJ4EBs+lVDR3veyJm+kXQ99b21/+jh5Xos1AnX5iItreGCc=
-----END CERTIFICATE-----`

const isrgRootX2PEM = `-----BEGIN CERTIFICATE-----
MIICGzCCAaGgAwIBAgIQQdKd0XLq7qeAwSxs6S+HUjAKBggqhkjOPQQDAzBPMQsw
CQYDVQQGEwJVUzEpMCcGA1UEChMgSW50ZXJuZXQgU2VjdXJpdHkgUmVzZWFyY2gg
R3JvdXAxFTATBgNVBAMTDElTUkcgUm9vdCBYMjAeFw0yMDA5MDQwMDAwMDBaFw00
MDA5MTcxNjAwMDBaME8xCzAJBgNVBAYTAlVTMSkwJwYDVQQKEyBJbnRlcm5ldCBT
ZWN1cml0eSBSZXNlYXJjaCBHcm91cDEVMBMGA1UEAxMMSVNSRyBSb290IFgyMHYw
EAYHKoZIzj0CAQYFK4EEACIDYgAEzZvVn4CDCuwJSvMWSj5cz3es3mcFDR0HttwW
+1qLFNvicWDEukWVEYmO6gbf9yoWHKS5xcUy4APgHoIYOIvXRdgKam7mAHf7AlF9
ItgKbppbd9/w+kHsOdx1ymgHDB/qo0IwQDAOBgNVHQ8BAf8EBAMCAQYwDwYDVR0T
AQH/BAUwAwEB/zAdBgNVHQ4EFgQUfEKWrt5LSDv6kviejM9ti6lyN5UwCgYIKoZI
zj0EAwMDaAAwZQIwe3lORlCEwkSHRhtFcP9Ymd70/aTSVaYgLXTWNLxBo1BfASdW
tL4ndQavEi51mI38AjEAi/V3bNTIZargCyzuFJ0nN6T5U6VR5CmD1/iQMVtCnwr1
/q4AaOeMSQ+2b1tbFfLn
-----END CERTIFICATE-----`

var (
	isrgRootPool   *x509.CertPool
	isrgRootDERSet = map[string]bool{}
)

func init() {
	isrgRootPool = x509.NewCertPool()
	for _, rawPEM := range []string{isrgRootX1PEM, isrgRootX2PEM} {
		for _, c := range parsePEMCertificates([]byte(rawPEM)) {
			isrgRootPool.AddCert(c)
			isrgRootDERSet[string(c.Raw)] = true
		}
	}
}

func parsePEMCertificates(data []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	for rest := data; len(rest) > 0; {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			certs = append(certs, cert)
		}
	}
	return certs
}

func isSelfSignedCert(c *x509.Certificate) bool {
	return c != nil && bytes.Equal(c.RawIssuer, c.RawSubject) && c.CheckSignatureFrom(c) == nil
}

func isLetsEncryptProdCert(leaf *x509.Certificate, domainInfo M, domainCert *tls.Certificate) bool {
	if leaf == nil || isSelfSignedCert(leaf) {
		return false
	}
	if strings.Contains(strings.ToLower(leaf.Issuer.String()), "staging") {
		return false
	}
	if domainCert != nil && len(domainCert.Certificate) > 0 && bytes.Equal(leaf.Raw, domainCert.Certificate[0]) {
		if boolean(domainInfo["staging"]) {
			return false
		}
		interPool := x509.NewCertPool()
		for _, der := range domainCert.Certificate[1:] {
			if c, err := x509.ParseCertificate(der); err == nil {
				interPool.AddCert(c)
			}
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:         isrgRootPool,
			Intermediates: interPool,
			CurrentTime:   leaf.NotBefore,
		}); err == nil {
			return true
		}
	}
	return slices.Contains(leaf.Issuer.Organization, "Let's Encrypt")
}

func tlsCertChainPEM(cert *tls.Certificate) []byte {
	if cert == nil {
		return nil
	}
	var chain []byte
	for _, der := range cert.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	return chain
}

func extractIntermediatePEMs(chainPEM []byte) [][]byte {
	var out [][]byte
	seen := map[string]bool{}
	for idx, c := range parsePEMCertificates(chainPEM) {
		if idx == 0 || !c.IsCA || isSelfSignedCert(c) || isrgRootDERSet[string(c.Raw)] {
			continue
		}
		key := string(c.Raw)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return out
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.Trim(host, "[]"))
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func resolveSoftEtherChainCertsDir(cfg Config, host string) string {
	if explicit := strings.TrimSpace(cfg.SEChainCertsDir); explicit != "" {
		return explicit
	}
	if seDir := strings.TrimSpace(cfg.SEDir); seDir != "" && dirExists(seDir) {
		return filepath.Join(seDir, "chain_certs")
	}
	if !isLoopbackHost(host) {
		return ""
	}
	for _, base := range cfg.ChainCertSearchDirs {
		if base == "" {
			continue
		}
		if fileExists(filepath.Join(base, "vpnserver")) ||
			fileExists(filepath.Join(base, "vpn_server.config")) ||
			dirExists(filepath.Join(base, "chain_certs")) {
			return filepath.Join(base, "chain_certs")
		}
	}
	return ""
}

func writeIntermediateChainFiles(dir string, chainPEM []byte) (bool, error) {
	intermediates := extractIntermediatePEMs(chainPEM)
	if len(intermediates) == 0 {
		matches, _ := filepath.Glob(filepath.Join(dir, "sem-chain-*.cer"))
		for _, m := range matches {
			_ = os.Remove(m)
		}
		_ = os.Remove(filepath.Join(dir, ".synced"))
		return false, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	changed := false
	keep := map[string]bool{}
	for i, certPEM := range intermediates {
		name := fmt.Sprintf("sem-chain-%d.cer", i+1)
		target := filepath.Join(dir, name)
		keep[target] = true
		existing, err := os.ReadFile(target)
		if err == nil && bytes.Equal(existing, certPEM) {
			continue
		}
		if err := atomicWrite(target, certPEM, 0644); err != nil {
			return false, err
		}
		changed = true
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "sem-chain-*.cer"))
	for _, m := range matches {
		if !keep[m] {
			_ = os.Remove(m)
			changed = true
		}
	}
	if changed {
		_ = os.Remove(filepath.Join(dir, ".synced"))
	}
	return true, nil
}

func syncSoftEtherChainCerts(cfg Config, host string, chainPEM []byte) (bool, error) {
	dir := resolveSoftEtherChainCertsDir(cfg, host)
	if dir == "" {
		return false, nil
	}
	return writeIntermediateChainFiles(dir, chainPEM)
}

func hasSyncedChainCerts(cfg Config, host string) bool {
	dir := resolveSoftEtherChainCertsDir(cfg, host)
	if dir == "" {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if strings.HasSuffix(lower, ".cer") || strings.HasSuffix(lower, ".crt") || strings.HasSuffix(lower, ".pem") {
			if info, err := entry.Info(); err == nil && info.Size() > 0 {
				return true
			}
		}
	}
	return false
}

func extractOpenVPNCABlock(config string) (start, end int, body string, eol string, lines []string, ok bool) {
	eol = "\n"
	if strings.Contains(config, "\r\n") {
		eol = "\r\n"
	}
	rawLines := strings.Split(config, "\n")
	lines = make([]string, len(rawLines))
	start, end = -1, -1
	for i, line := range rawLines {
		trimmed := strings.TrimSuffix(line, "\r")
		lines[i] = trimmed
		tag := strings.TrimSpace(trimmed)
		if start == -1 && strings.EqualFold(tag, "<ca>") {
			start = i
			continue
		}
		if start != -1 && strings.EqualFold(tag, "</ca>") {
			end = i
			break
		}
	}
	if start == -1 || end <= start+1 {
		return 0, 0, "", eol, nil, false
	}
	body = strings.Join(lines[start+1:end], "\n")
	return start, end, body, eol, lines, true
}

func replaceOpenVPNCABlock(lines []string, start, end int, eol string, pemBlocks [][]byte) string {
	var replacement []string
	for _, block := range pemBlocks {
		text := strings.TrimSpace(strings.ReplaceAll(string(block), "\r\n", "\n"))
		if text == "" {
			continue
		}
		replacement = append(replacement, strings.Split(text, "\n")...)
	}
	updated := make([]string, 0, len(lines)-(end-start-1)+len(replacement))
	updated = append(updated, lines[:start+1]...)
	updated = append(updated, replacement...)
	updated = append(updated, lines[end:]...)
	return strings.Join(updated, eol)
}

func (a *App) applyOpenVPNCA(ctx context.Context, config string) string {
	start, end, body, eol, lines, ok := extractOpenVPNCABlock(config)
	if !ok {
		return config
	}
	embedded := parsePEMCertificates([]byte(body))
	if len(embedded) == 0 {
		return config
	}
	var domainInfo M
	var domainCert *tls.Certificate
	if a.tls != nil {
		if domain := a.tls.effectiveDomain(); domain != "" {
			domainInfo, domainCert = a.tls.certificate(domain)
		}
	}
	if !isLetsEncryptProdCert(embedded[0], domainInfo, domainCert) {
		return config
	}

	conn := a.connection(ctx)
	serverHost := str(conn["host"])
	serverID := a.serverIDFromContext(ctx)
	matchesManaged := domainCert != nil && !boolean(domainInfo["staging"]) &&
		len(domainCert.Certificate) > 0 && bytes.Equal(embedded[0].Raw, domainCert.Certificate[0])

	if matchesManaged && a.tls != nil {
		key := fmt.Sprintf("%d:%s:%s", serverID, a.tls.effectiveDomain(), certFingerprint(domainCert.Certificate[0]))
		a.tls.syncChainCertsForServer(serverHost, key, tlsCertChainPEM(domainCert))
	}
	chainSynced := hasSyncedChainCerts(a.cfg, serverHost)
	if !chainSynced && matchesManaged {
		if domain := a.tls.effectiveDomain(); domain != "" {
			if a.tls.isRemoteChainSynced(serverID, domain, certFingerprint(domainCert.Certificate[0])) ||
				fileExists(filepath.Join(a.cfg.DataDir, "ssl", "chain", domain, ".synced")) {
				chainSynced = true
			}
		}
	}

	blocks := [][]byte{[]byte(isrgRootX1PEM), []byte(isrgRootX2PEM)}
	if !chainSynced && matchesManaged {
		blocks = append(blocks, extractIntermediatePEMs(tlsCertChainPEM(domainCert))...)
	}
	return replaceOpenVPNCABlock(lines, start, end, eol, blocks)
}
