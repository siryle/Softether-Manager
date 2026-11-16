package manager

import (
	"net/http"
	"strings"
	"time"
	"unicode"
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
		for k, v := range changes {
			a.set(k, v)
		}
		a.audit(u, "settings.updated", "panel", "", "")
		out := a.settings()
		_, out["restart_required"] = changes["web_path"]
		return out
	})
	a.endpoint("GET /system/audit", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		sql := `SELECT AuditLogID id,Username username,Action action,TargetType target_type,TargetKey target_key,Detail detail,CreatedDate created_date FROM AuditLog`
		args := []any{}
		if before := queryInt(r, "before_id", 0, 0, 1<<63-1); before > 0 {
			sql += " WHERE AuditLogID<?"
			args = append(args, before)
		}
		sql += " ORDER BY AuditLogID DESC LIMIT ?"
		args = append(args, queryInt(r, "limit", 100, 1, 500))
		return query(a.db, sql, args...)
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
		for _, key := range []string{"domain_only", "acme_staging"} {
			if v, ok := body[key]; ok && v != nil {
				if _, valid := v.(bool); !valid {
					fail(422, key+" must be a boolean.")
				}
			}
		}
		if boolean(body["domain_only"]) && !boolean(a.setting("domain_only")) {
			if domain == "" || hostName(r.Host) != domain {
				fail(422, "Open the panel through its domain before restricting access to that domain.")
			}
		}
		changed := []string{}
		if domain != current {
			a.set("domain", domain)
			changed = append(changed, "domain")
			if body["domain_only"] == nil {
				a.set("domain_only", false)
			}
			a.tls.mu.Lock()
			a.tls.nextAttempt = zeroTime()
			a.tls.state = M{}
			a.tls.mu.Unlock()
		}
		for _, key := range []string{"https_port", "acme_email", "acme_staging", "domain_only"} {
			if v, ok := body[key]; ok && v != nil {
				if key == "acme_email" {
					v = strings.TrimSpace(str(v))
				}
				if domain == "" && key == "domain_only" && boolean(v) {
					fail(422, "Set a domain first.")
				}
				a.set(key, v)
				changed = append(changed, key)
			}
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
		if str(a.setting("domain")) == "" {
			fail(422, "Set a domain first.")
		}
		func() {
			a.tls.mu.Lock()
			defer a.tls.mu.Unlock()
			if a.tls.busy || a.tls.requested {
				fail(409, "A certificate request is already running.")
			}
			a.tls.requested = true
		}()
		a.audit(u, "domain.certificate_requested", "panel", str(a.setting("domain")), "")
		out := a.tls.status(r)
		out["busy"], out["phase"] = true, "issuing"
		return out
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
