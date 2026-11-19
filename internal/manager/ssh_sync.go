package manager

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshSyncConfig struct {
	Host      string
	Port      int
	User      string
	AuthType  string
	KeyPEM    string
	Password  string
	RemoteDir string
	ResetPin  bool
}

func generateEd25519SSHKeyPair() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "softether-manager")
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	privPEM := string(pem.EncodeToMemory(block))
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	return privPEM, pubLine, nil
}

func sshPublicKeyLine(privatePEM string) string {
	trimmed := strings.TrimSpace(privatePEM)
	if trimmed == "" {
		return ""
	}
	signer, err := ssh.ParsePrivateKey([]byte(trimmed))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

func validateSSHHost(host string) string {
	h := strings.TrimSpace(host)
	if strings.ContainsAny(h, " \t\r\n\x00/@") {
		fail(422, "Invalid SSH host.")
	}
	return h
}

func validateSSHRemoteDir(dir string) string {
	d := strings.TrimSpace(dir)
	if strings.ContainsAny(d, "\r\n\x00") {
		fail(422, "Invalid remote chain_certs directory path.")
	}
	return d
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (a *App) sshConfigFor(ctx context.Context, overrides M) (sshSyncConfig, error) {
	host, hasHost := "", false
	if v, ok := overrides["cert_ssh_host"]; ok && v != nil {
		hasHost = true
		host = strings.TrimSpace(str(v))
	}
	if !hasHost {
		host = strings.TrimSpace(str(a.setting("cert_ssh_host")))
	}
	if host == "" {
		_ = recoverAsError(func() error {
			host = strings.TrimSpace(str(a.connection(ctx)["host"]))
			return nil
		})
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00/@") {
		return sshSyncConfig{}, httpError{422, "A valid SSH host is required."}
	}

	port := int(integer(overrides["cert_ssh_port"]))
	if port == 0 {
		port = int(integer(a.setting("cert_ssh_port")))
	}
	if port <= 0 || port > 65535 {
		port = 22
	}

	user, hasUser := "", false
	if v, ok := overrides["cert_ssh_user"]; ok && v != nil {
		hasUser = true
		user = strings.TrimSpace(str(v))
	}
	if !hasUser {
		user = strings.TrimSpace(str(a.setting("cert_ssh_user")))
	}
	if user == "" {
		user = "root"
	}
	if strings.ContainsAny(user, " \t\r\n\x00@") {
		return sshSyncConfig{}, httpError{422, "Invalid SSH username."}
	}

	authType := strings.TrimSpace(str(overrides["cert_ssh_auth_type"]))
	if authType == "" {
		authType = strings.TrimSpace(str(a.setting("cert_ssh_auth_type")))
	}
	if authType != "password" {
		authType = "key"
	}

	keyPEM := strings.TrimSpace(str(overrides["cert_ssh_key"]))
	if keyPEM == "" {
		keyPEM = a.secretSetting("cert_ssh_key")
	}
	password := str(overrides["cert_ssh_password"])
	if strings.TrimSpace(password) == "" {
		password = a.secretSetting("cert_ssh_password")
	}

	remoteDir := strings.TrimSpace(str(overrides["cert_ssh_remote_dir"]))
	if _, hasOverride := overrides["cert_ssh_remote_dir"]; !hasOverride {
		remoteDir = strings.TrimSpace(str(a.setting("cert_ssh_remote_dir")))
	}
	if strings.ContainsAny(remoteDir, "\r\n\x00") {
		return sshSyncConfig{}, httpError{422, "Invalid remote chain_certs directory path."}
	}

	return sshSyncConfig{
		Host:      host,
		Port:      port,
		User:      user,
		AuthType:  authType,
		KeyPEM:    keyPEM,
		Password:  password,
		RemoteDir: remoteDir,
		ResetPin:  boolean(overrides["reset_host_key"]),
	}, nil
}

func (a *App) dialSSH(ctx context.Context, cfg sshSyncConfig) (*ssh.Client, string, error) {
	var authMethods []ssh.AuthMethod
	if cfg.AuthType == "password" {
		if strings.TrimSpace(cfg.Password) == "" {
			return nil, "", httpError{422, "An SSH password is required."}
		}
		pw := cfg.Password
		authMethods = append(authMethods,
			ssh.Password(pw),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = pw
				}
				return answers, nil
			}),
		)
	} else {
		if strings.TrimSpace(cfg.KeyPEM) == "" {
			return nil, "", httpError{422, "An SSH private key is required (paste one or generate an Ed25519 key)."}
		}
		signer, err := ssh.ParsePrivateKey([]byte(cfg.KeyPEM))
		if err != nil {
			return nil, "", httpError{422, "Invalid SSH private key: " + err.Error()}
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}

	target := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	var hostFingerprint string
	clientCfg := &ssh.ClientConfig{
		User:    cfg.User,
		Auth:    authMethods,
		Timeout: 15 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := ssh.FingerprintSHA256(key)
			hostFingerprint = fp
			pinned := strings.TrimSpace(str(a.setting("cert_ssh_host_key")))
			prefix := target + " "
			if cfg.ResetPin || pinned == "" || !strings.HasPrefix(pinned, prefix) {
				a.set("cert_ssh_host_key", prefix+fp)
				return nil
			}
			if pinned != prefix+fp {
				return fmt.Errorf("SSH host key changed for %s (%s); reset the pinned host key if the server was rebuilt", target, fp)
			}
			return nil
		},
	}

	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(dialCtx, "tcp", target)
	if err != nil {
		return nil, "", fmt.Errorf("SSH connect %s: %w", target, err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-dialCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	c, chans, reqs, err := ssh.NewClientConn(conn, target, clientCfg)
	close(done)
	if err != nil {
		_ = conn.Close()
		return nil, "", fmt.Errorf("SSH handshake %s: %w", target, err)
	}
	return ssh.NewClient(c, chans, reqs), hostFingerprint, nil
}

func runSSHCommand(ctx context.Context, client *ssh.Client, command string, stdin []byte) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	session.Stdout = &out
	session.Stderr = &out

	errCh := make(chan error, 1)
	go func() {
		errCh <- session.Run(command)
	}()
	select {
	case err = <-errCh:
	case <-ctx.Done():
		_ = session.Close()
		err = ctx.Err()
	}
	text := strings.TrimSpace(out.String())
	if err != nil && text != "" {
		if len(text) > 300 {
			text = "..." + text[len(text)-300:]
		}
		return text, fmt.Errorf("%w (%s)", err, text)
	}
	return text, err
}

func resolveRemoteChainDirSSH(ctx context.Context, client *ssh.Client, requested string) (string, error) {
	dir := strings.TrimSpace(requested)
	if dir == "" {
		probeCmd := `for d in /var/lib/softether /opt/vpnserver /usr/local/vpnserver /usr/vpnserver /opt/softether/vpnserver /usr/local/libexec/softether/vpnserver; do if [ -d "$d/chain_certs" ] || [ -x "$d/vpnserver" ] || [ -f "$d/vpn_server.config" ]; then printf '%s' "$d/chain_certs"; exit 0; fi; done; exit 1`
		detected, err := runSSHCommand(ctx, client, probeCmd, nil)
		if err != nil || strings.TrimSpace(detected) == "" {
			return "", fmt.Errorf("could not auto-detect remote SoftEther chain_certs/ directory; enter it explicitly")
		}
		dir = strings.TrimSpace(detected)
	}
	q := shellQuote(dir)
	if _, err := runSSHCommand(ctx, client, fmt.Sprintf("mkdir -p %s && test -w %s", q, q), nil); err != nil {
		return "", fmt.Errorf("remote directory %s is not writable: %w", dir, err)
	}
	return dir, nil
}

func buildChainTar(intermediates [][]byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	nowTime := time.Now()
	for i, certPEM := range intermediates {
		hdr := &tar.Header{
			Name:    fmt.Sprintf("sem-chain-%d.cer", i+1),
			Mode:    0644,
			Size:    int64(len(certPEM)),
			ModTime: nowTime,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(certPEM); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (a *App) syncRemoteChainCertsSSH(ctx context.Context, cfg sshSyncConfig, chainPEM []byte) (string, string, error) {
	client, fp, err := a.dialSSH(ctx, cfg)
	if err != nil {
		return "", "", err
	}
	defer client.Close()

	remoteDir, err := resolveRemoteChainDirSSH(ctx, client, cfg.RemoteDir)
	if err != nil {
		return "", "", err
	}
	q := shellQuote(remoteDir)
	intermediates := extractIntermediatePEMs(chainPEM)
	if len(intermediates) == 0 {
		if _, err := runSSHCommand(ctx, client, fmt.Sprintf("rm -f %s/sem-chain-*.cer", q), nil); err != nil {
			return "", "", fmt.Errorf("clean remote chain_certs: %w", err)
		}
		return remoteDir, fp, nil
	}
	tarBytes, err := buildChainTar(intermediates)
	if err != nil {
		return "", "", err
	}
	cmd := fmt.Sprintf("mkdir -p %s && rm -f %s/sem-chain-*.cer && tar -C %s -xf - && chmod 644 %s/sem-chain-*.cer", q, q, q, q)
	if _, err := runSSHCommand(ctx, client, cmd, tarBytes); err != nil {
		return "", "", fmt.Errorf("upload remote chain_certs: %w", err)
	}
	return remoteDir, fp, nil
}

func (a *App) testRemoteSSHSync(ctx context.Context, overrides M) (string, string, error) {
	cfg, err := a.sshConfigFor(ctx, overrides)
	if err != nil {
		return "", "", err
	}
	client, fp, err := a.dialSSH(ctx, cfg)
	if err != nil {
		return "", "", err
	}
	defer client.Close()

	remoteDir, err := resolveRemoteChainDirSSH(ctx, client, cfg.RemoteDir)
	if err != nil {
		return "", "", err
	}
	return remoteDir, fp, nil
}
