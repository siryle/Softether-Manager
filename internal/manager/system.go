package manager

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/ssh"
)

var settingBounds = map[string][2]int64{"resource_interval_seconds": {1, 3600}, "resource_history_points": {10, 2000}, "sample_interval_minutes": {1, 1440}, "sample_retention_days": {1, 3650}, "session_interval_seconds": {5, 3600}, "session_history_retention_days": {1, 3650}, "quota_interval_seconds": {10, 3600}, "ui_live_seconds": {1, 3600}, "ui_detail_seconds": {1, 3600}, "ui_list_seconds": {1, 3600}, "update_check_interval_hours": {1, 168}}
var settingFlags = map[string]bool{"resource_monitor_enabled": true, "traffic_monitor_enabled": true, "session_monitor_enabled": true, "session_traffic_enabled": true, "quota_enforcement_enabled": true, "update_check_enabled": true}

func cleanWebPath(value string) string {
	path := strings.Trim(strings.TrimSpace(value), "/")
	if len([]rune(path)) > 64 {
		fail(422, "Web path is too long.")
	}
	for _, c := range path {
		if !unicode.IsLetter(c) && !unicode.IsNumber(c) && !strings.ContainsRune("._~-", c) {
			fail(422, "The web path may contain only letters, digits, dot, underscore, tilde and hyphen.")
		}
	}
	return path
}
func (a *App) registerSystem() {
	a.handle("GET /api/v1/system/health", true, func(_ http.ResponseWriter, _ *http.Request, _ M) any { return M{"ok": true, "version": a.cfg.Version} })
	a.endpoint("GET /system/resources", func(_ http.ResponseWriter, _ *http.Request, _ M) any { return a.resourceSnapshot() })
	a.endpoint("GET /system/info", func(_ http.ResponseWriter, _ *http.Request, _ M) any {
		return M{"version": a.cfg.Version, "release_repo": a.cfg.Repo, "service": a.serviceStatus(), "env_file": env("SEM_ENV_FILE", "/etc/softether-manager.env")}
	})
	a.endpoint("GET /system/settings", func(_ http.ResponseWriter, _ *http.Request, _ M) any { return a.settings() })
	a.endpoint("PUT /system/settings", func(w http.ResponseWriter, r *http.Request, u M) any {
		body := readBody(w, r)
		changes := M{}
		for k, v := range body {
			if v == nil {
				continue
			}
			if k == "web_path" {
				s, ok := v.(string)
				if !ok {
					fail(422, "web_path must be a string.")
				}
				changes[k] = cleanWebPath(s)
			} else if b, ok := settingBounds[k]; ok {
				n := integer(v)
				if number(v) != float64(n) || n < b[0] || n > b[1] {
					fail(422, "Setting outside allowed range: "+k)
				}
				changes[k] = n
			} else if settingFlags[k] {
				on, ok := v.(bool)
				if !ok {
					fail(422, "Expected boolean setting: "+k)
				}
				changes[k] = on
			} else {
				fail(422, "Unknown panel setting: "+k)
			}
		}
		a.setSettings(changes)
		a.audit(u, "settings.updated", "panel", "", "")
		out := a.settings()
		_, out["restart_required"] = changes["web_path"]
		return out
	})
	a.endpoint("GET /system/audit", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		sql := `SELECT AuditLogID id,Username username,Action action,TargetType target_type,TargetKey target_key,Detail detail,CreatedDate created_date,ServerID server_id FROM AuditLog WHERE (ServerID=? OR ServerID IS NULL)`
		args := []any{a.serverIDFromContext(r.Context())}
		if before := queryInt(r, "before_id", 0, 0, 1<<63-1); before > 0 {
			sql += " AND AuditLogID<?"
			args = append(args, before)
		}
		sql += " ORDER BY AuditLogID DESC LIMIT ?"
		args = append(args, queryInt(r, "limit", 100, 1, 500))
		return query(a.readDB(), sql, args...)
	})
	a.endpoint("GET /system/ip-geo/status", func(_ http.ResponseWriter, _ *http.Request, _ M) any {
		a.geoMu.RLock()
		hasXDB := a.xdb != nil
		xdbPath := a.xdbPath
		a.geoMu.RUnlock()
		var count int64
		if row := one(a.db, `SELECT COUNT(*) n FROM IPGeoCache`); row != nil {
			count = integer(row["n"])
		}
		return M{"xdb_loaded": hasXDB, "xdb_path": xdbPath, "cached_count": count}
	})
	a.endpoint("POST /system/ip-geo/clear-cache", func(_ http.ResponseWriter, _ *http.Request, u M) any {
		n := a.clearIPGeoCache()
		a.audit(u, "ip_geo.cache_cleared", "system", "", fmt.Sprintf("cleared %d entries", n))
		return M{"ok": true, "cleared": n}
	})
	a.endpoint("GET /system/domain", func(_ http.ResponseWriter, r *http.Request, _ M) any { return a.tls.status(r) })
	a.endpoint("PUT /system/domain", func(w http.ResponseWriter, r *http.Request, u M) any {
		body := readBody(w, r)
		current := str(a.setting("domain"))
		domain := current
		if v, ok := body["domain"]; ok && v != nil {
			domain = normalizeDomain(str(v))
		}
		if v, ok := body["https_port"]; ok && v != nil {
			port := integer(v)
			if port < 1 || port > 65535 || port == 80 || port == int64(a.cfg.BindPort) {
				fail(422, "HTTPS needs its own port, distinct from HTTP and port 80.")
			}
		}
		if v, ok := body["acme_email"]; ok && v != nil {
			email := strings.TrimSpace(str(v))
			if len(email) > 254 || (email != "" && (!strings.Contains(email, "@") || strings.ContainsAny(email, " \r\n") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@"))) {
				fail(422, "Invalid email address.")
			}
		}
		for _, key := range []string{"domain_only", "acme_staging", "cert_auto_push", "cert_ssh_sync"} {
			if v, ok := body[key]; ok && v != nil {
				if _, valid := v.(bool); !valid {
					fail(422, key+" must be a boolean.")
				}
			}
		}
		if v, ok := body["cert_ssh_port"]; ok && v != nil {
			p := integer(v)
			if number(v) != float64(p) || p < 1 || p > 65535 {
				fail(422, "SSH port must be between 1 and 65535.")
			}
		}
		if v, ok := body["cert_ssh_auth_type"]; ok && v != nil {
			authType := strings.TrimSpace(str(v))
			if authType != "key" && authType != "password" {
				fail(422, "SSH auth type must be key or password.")
			}
		}
		if boolean(body["domain_only"]) && !boolean(a.setting("domain_only")) {
			if domain == "" || hostName(r.Host) != domain {
				fail(422, "Open the panel through its domain before restricting access to that domain.")
			}
		}
		changes := M{}
		changed := []string{}
		if domain != current {
			changes["domain"] = domain
			changed = append(changed, "domain")
			if body["domain_only"] == nil {
				changes["domain_only"] = false
			}
		}
		for _, key := range []string{
			"https_port", "acme_email", "acme_staging", "domain_only", "acme_challenge_type", "cf_dns_token",
			"cert_key_type", "cert_auto_push", "cert_renew_hook",
			"cert_ssh_sync", "cert_ssh_host", "cert_ssh_port", "cert_ssh_user", "cert_ssh_auth_type", "cert_ssh_key", "cert_ssh_password", "cert_ssh_remote_dir",
		} {
			v, ok := body[key]
			if !ok || v == nil {
				continue
			}
			switch key {
			case "acme_email":
				v = strings.TrimSpace(str(v))
			case "cf_dns_token", "cert_ssh_key", "cert_ssh_password":
				plain := strings.TrimSpace(str(v))
				if key == "cert_ssh_key" && plain != "" {
					if _, err := ssh.ParsePrivateKey([]byte(plain)); err != nil {
						fail(422, "Invalid SSH private key: "+err.Error())
					}
				}
				v = ""
				if plain != "" {
					v = "enc:" + a.encrypt(plain)
				}
			case "cert_ssh_host":
				v = validateSSHHost(str(v))
			case "cert_ssh_user":
				uName := strings.TrimSpace(str(v))
				if strings.ContainsAny(uName, " \t\r\n\x00@") {
					fail(422, "Invalid SSH username.")
				}
				if uName == "" {
					uName = "root"
				}
				v = uName
			case "cert_ssh_remote_dir":
				v = validateSSHRemoteDir(str(v))
			case "cert_renew_hook":
				v = validateCertRenewHook(a.cfg.Root, a.cfg.DataDir, str(v))
			}
			if domain == "" && key == "domain_only" && boolean(v) {
				fail(422, "Set a domain first.")
			}
			changes[key] = v
			changed = append(changed, key)
		}
		if boolean(body["reset_cert_ssh_host_key"]) ||
			str(withDefault(changes, "cert_ssh_host", a.setting("cert_ssh_host"))) != str(a.setting("cert_ssh_host")) ||
			integer(withDefault(changes, "cert_ssh_port", a.setting("cert_ssh_port"))) != integer(a.setting("cert_ssh_port")) {
			changes["cert_ssh_host_key"] = ""
		}
		if v, ok := body["cert_push_server_id"]; ok && v != nil {
			id := integer(v)
			if id < 0 || number(v) != float64(id) {
				fail(422, "Invalid certificate deployment server ID.")
			}
			if id > 0 && !boolean(a.serverRow(id)["IsEnabled"]) {
				fail(422, "The certificate deployment server must be enabled.")
			}
			changes["cert_push_server_id"] = id
			changed = append(changed, "cert_push_server_id")
		}
		a.setSettings(changes)
		if domain != current {
			a.tls.mu.Lock()
			a.tls.nextAttempt = zeroTime()
			a.tls.state = M{}
			a.tls.mu.Unlock()
		}
		a.tls.apply()
		a.audit(u, "domain.updated", "panel", domain, strings.Join(changed, ", "))
		out := a.tls.status(r)
		out["changed"] = changed
		return out
	})
	a.endpoint("DELETE /system/domain", func(_ http.ResponseWriter, r *http.Request, u M) any {
		old := str(a.setting("domain"))
		a.set("domain", "")
		a.set("domain_only", false)
		a.tls.apply()
		a.audit(u, "domain.removed", "panel", old, "")
		return a.tls.status(r)
	})
	a.endpoint("POST /system/domain/issue", func(_ http.ResponseWriter, r *http.Request, u M) any {
		domain := str(a.setting("domain"))
		if domain == "" {
			fail(422, "Set a certificate domain first.")
		}
		func() {
			a.tls.mu.Lock()
			defer a.tls.mu.Unlock()
			if a.tls.busy || a.tls.requested {
				fail(409, "A certificate request is already running.")
			}
			a.tls.requested = true
		}()
		a.audit(u, "domain.certificate_requested", "panel", domain, "")
		out := a.tls.status(r)
		out["busy"], out["phase"] = true, "issuing"
		return out
	})
	a.endpoint("POST /system/domain/push-cert", func(_ http.ResponseWriter, r *http.Request, u M) any {
		domain := a.tls.effectiveDomain()
		if domain == "" {
			fail(422, "Set a domain or server public host first.")
		}
		check(a.tls.pushDomainCertificate(r.Context(), domain))
		a.audit(u, "domain.certificate_pushed", "server", domain, "SetServerCert")
		return M{"ok": true, "message": "Certificate pushed to SoftEther VPN Server successfully."}
	})
	a.endpoint("POST /system/domain/ssh-keygen", func(_ http.ResponseWriter, _ *http.Request, u M) any {
		privPEM, pubLine, err := generateEd25519SSHKeyPair()
		check(err)
		a.setSecretSetting("cert_ssh_key", privPEM)
		a.set("cert_ssh_auth_type", "key")
		a.audit(u, "domain.ssh_key_generated", "panel", "cert_ssh_key", "")
		return M{"ok": true, "public_key": pubLine, "has_cert_ssh_key": true}
	})
	a.endpoint("POST /system/domain/test-ssh", func(w http.ResponseWriter, r *http.Request, _ M) any {
		body := readBody(w, r)
		remoteDir, fp, err := a.testRemoteSSHSync(r.Context(), body)
		if err != nil {
			if _, ok := err.(httpError); ok {
				panic(err)
			}
			fail(502, err.Error())
		}
		return M{"ok": true, "remote_dir": remoteDir, "fingerprint": fp, "host_key": a.setting("cert_ssh_host_key")}
	})
	a.endpoint("POST /system/domain/check", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		domain := str(a.setting("domain"))
		if domain != "" {
			a.tls.checkDNS(r.Context(), domain)
			a.tls.apply()
		}
		return a.tls.status(r)
	})
	for _, action := range []string{"status", "check"} {
		pattern := "GET /system/update"
		if action == "check" {
			pattern = "POST /system/update/check"
		}
		a.endpoint(pattern, func(_ http.ResponseWriter, r *http.Request, _ M) any {
			return a.updateStatus(r.Context(), action == "check")
		})
	}
	a.endpoint("POST /system/update/apply", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		return a.applyUpdate(r.Context(), str(b["version"]), u)
	})
	a.endpoint("GET /system/update/state", func(_ http.ResponseWriter, _ *http.Request, _ M) any { return a.updateRunState() })
	a.endpoint("POST /system/restart", func(_ http.ResponseWriter, r *http.Request, u M) any {
		a.restart(r.Context())
		a.audit(u, "panel.restarted", "panel", "", "")
		return M{"ok": true, "detail": "Restarting in a moment."}
	})
}
func zeroTime() (t time.Time) { return }
