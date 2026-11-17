package manager

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func (a *App) SetPassword(username, password string) {
	if utf8.RuneCountInString(username) < 1 || utf8.RuneCountInString(username) > 64 || utf8.RuneCountInString(password) < 1 || utf8.RuneCountInString(password) > 256 {
		fail(422, "Username or password length is invalid.")
	}
	hash, err := HashPassword(password)
	check(err)
	execute(a.db, `INSERT INTO PanelUser(Username,PasswordHash,CreatedDate,UpdatedDate) VALUES(?,?,?,?) ON CONFLICT(Username) DO UPDATE SET PasswordHash=excluded.PasswordHash,SessionVersion=PanelUser.SessionVersion+1,IsDeleted=0,UpdatedDate=excluded.UpdatedDate`, username, hash, now(), now())
}
func (a *App) issue(w http.ResponseWriter, r *http.Request, user M) M {
	token := a.token(integer(user["UserID"]), str(user["Username"]), integer(user["SessionVersion"]))
	http.SetCookie(w, &http.Cookie{Name: "sem_session", Value: token, Path: "/", MaxAge: a.cfg.SessionMinutes * 60, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	return M{"username": user["Username"]}
}
func (a *App) registerAuth() {
	a.handle("GET /api/v1/auth/state", true, func(_ http.ResponseWriter, _ *http.Request, _ M) any {
		return M{"setup_required": integer(one(a.db, `SELECT COUNT(*) n FROM PanelUser WHERE IsDeleted=0`)["n"]) == 0}
	})
	for _, action := range []string{"setup", "login"} {
		a.handle("POST /api/v1/auth/"+action, true, func(w http.ResponseWriter, r *http.Request, _ M) any {
			body := readBody(w, r)
			username, password := str(body["username"]), str(body["password"])
			if len(username) == 0 || utf8.RuneCountInString(username) > 64 || len(password) == 0 || utf8.RuneCountInString(password) > 256 {
				fail(422, "Username and password are required (maximum 64 / 256 characters).")
			}
			if action == "setup" {
				a.setupMu.Lock()
				defer a.setupMu.Unlock()
				if integer(one(a.db, `SELECT COUNT(*) n FROM PanelUser WHERE IsDeleted=0`)["n"]) > 0 {
					fail(409, "An account already exists on this panel.")
				}
				a.SetPassword(username, password)
				user := one(a.db, `SELECT UserID,Username,SessionVersion FROM PanelUser WHERE Username=? AND IsDeleted=0`, username)
				a.audit(user, "auth.setup", "panel_user", username, "")
				return a.issue(w, r, user)
			}
			ip := loginIP(r)
			if !a.logins.take(ip, time.Now()) {
				w.Header().Set("Retry-After", "300")
				fail(429, "Too many login attempts. Try again in five minutes.")
			}
			execute(a.db, `DELETE FROM RevokedSession WHERE ExpiresAt<=?`, time.Now().Unix())
			user := one(a.db, `SELECT * FROM PanelUser WHERE Username=? AND IsDeleted=0`, username)
			if user == nil || !VerifyPassword(password, str(user["PasswordHash"])) {
				fail(401, "Wrong username or password.")
			}
			a.logins.clear(ip)
			return a.issue(w, r, user)
		})
	}
	a.handle("POST /api/v1/auth/logout", true, func(w http.ResponseWriter, r *http.Request, _ M) any {
		token := requestToken(r)
		if claims := a.claims(token); claims != nil {
			execute(a.db, `DELETE FROM RevokedSession WHERE ExpiresAt<=?`, time.Now().Unix())
			execute(a.db, `INSERT INTO RevokedSession(TokenHash,ExpiresAt) VALUES(?,?) ON CONFLICT DO NOTHING`, tokenHash(token), integer(claims["exp"]))
		}
		http.SetCookie(w, &http.Cookie{Name: "sem_session", Path: "/", Value: "", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
		return M{"ok": true}
	})
	a.handle("GET /api/v1/auth/me", false, func(_ http.ResponseWriter, _ *http.Request, user M) any { return M{"username": user["Username"]} })
	a.handle("PUT /api/v1/auth/password", false, func(w http.ResponseWriter, r *http.Request, user M) any {
		b := readBody(w, r)
		row := one(a.db, `SELECT PasswordHash FROM PanelUser WHERE UserID=?`, user["UserID"])
		if row == nil || !VerifyPassword(str(b["current_password"]), str(row["PasswordHash"])) {
			fail(403, "The current password is wrong.")
		}
		a.SetPassword(str(user["Username"]), str(b["new_password"]))
		a.audit(user, "auth.password_changed", "panel_user", str(user["Username"]), "")
		a.issue(w, r, one(a.db, `SELECT UserID,Username,SessionVersion FROM PanelUser WHERE UserID=?`, user["UserID"]))
		return M{"ok": true}
	})
}
func connectionInput(body M) (string, int, string) {
	host := strings.TrimSpace(str(withDefault(body, "host", "127.0.0.1")))
	port := int(integer(withDefault(body, "port", 5555)))
	auth := str(body["auth"])
	if host == "" || len(host) > 255 || strings.ContainsAny(host, "/\r\n") || port < 1 || port > 65535 || len(str(body["password"])) > 256 {
		fail(422, "Invalid SoftEther host, port or password.")
	}
	if auth != "" && auth != "header" && auth != "basic" && auth != "both" {
		fail(422, "Authentication must be header, basic or both.")
	}
	return host, port, auth
}
func (a *App) registerConnection() {
	a.handle("GET /api/v1/server/connection", false, func(_ http.ResponseWriter, r *http.Request, _ M) any { return a.connection(r.Context()) })
	for _, action := range []string{"save", "test"} {
		pattern := "PUT /api/v1/server/connection"
		if action == "test" {
			pattern = "POST /api/v1/server/connection/test"
		}
		a.handle(pattern, false, func(w http.ResponseWriter, r *http.Request, user M) any {
			body := readBody(w, r)
			if action == "save" && body["host"] == nil {
				if v, ok := body["public_host"]; ok && v != nil {
					h := strings.TrimSpace(str(v))
					a.set("vpn_public_host", h)
					if str(a.setting("domain")) == "" && h != "" {
						a.set("domain", h)
					}
				}
				if v, ok := body["public_port"]; ok && v != nil {
					p := integer(v)
					if p >= 1 && p <= 65535 {
						a.set("vpn_public_port", p)
					}
				}
				a.audit(user, "connection.updated", "server", "", "public_info")
				return a.connection(r.Context())
			}
			host, port, auth := connectionInput(body)
			row := a.serverRow(a.serverIDFromContext(r.Context()))
			password, err := a.decrypt(str(row["EncryptedPassword"]))
			check(err)
			var pwd *string
			if v, ok := body["password"]; ok && v != nil {
				p := str(v)
				if action != "test" || p != "" {
					password = p
				}
				pwd = &p
			}
			if action == "test" {
				return a.testConnection(r.Context(), host, port, password, auth)
			}
			if !boolean(body["skip_test"]) {
				out := a.testConnection(r.Context(), host, port, password, auth)
				auth = str(out["auth"])
			}
			if v, ok := body["public_host"]; ok && v != nil {
				h := strings.TrimSpace(str(v))
				a.set("vpn_public_host", h)
				if str(a.setting("domain")) == "" && h != "" {
					a.set("domain", h)
				}
			}
			if v, ok := body["public_port"]; ok && v != nil {
				p := integer(v)
				if p >= 1 && p <= 65535 {
					a.set("vpn_public_port", p)
				}
			}
			a.saveConnection(host, port, pwd, auth, r.Context())
			a.audit(user, "connection.updated", "server", "", host)
			return a.connection(r.Context())
		})
	}
	a.handle("POST /api/v1/server/probe", false, func(_ http.ResponseWriter, r *http.Request, _ M) any { return a.probeServer(r.Context()) })
}
func fmtError(v any) string {
	if err, ok := v.(httpError); ok {
		if m, ok := err.Detail.(map[string]any); ok {
			return str(m["message"])
		}
		return str(err.Detail)
	}
	return str(v)
}
