package manager

import (
	"context"
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func serverID(r *http.Request) int64 {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		fail(422, "Invalid server id.")
	}
	return id
}

func (a *App) serverRow(id int64) M {
	row := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, id)
	if row == nil {
		fail(404, "Server not found.")
	}
	return row
}

func publicServer(row M) M {
	return M{"id": row["ServerID"], "name": row["Name"], "host": row["Host"], "port": row["Port"], "auth": row["AuthType"], "is_enabled": boolean(row["IsEnabled"]), "is_default": boolean(row["IsDefault"]), "skip_cert_check": boolean(row["SkipCertCheck"]), "sort_order": row["SortOrder"], "configured": str(row["EncryptedPassword"]) != "", "created_date": row["CreatedDate"], "updated_date": row["UpdatedDate"]}
}

func (a *App) serverInput(existing, body M) M {
	row := clone(existing)
	for key, column := range map[string]string{"name": "Name", "host": "Host", "port": "Port", "auth": "AuthType", "is_enabled": "IsEnabled", "is_default": "IsDefault", "skip_cert_check": "SkipCertCheck", "sort_order": "SortOrder"} {
		if v, ok := body[key]; ok {
			row[column] = v
		}
	}
	row["Name"] = strings.TrimSpace(str(row["Name"]))
	if utf8.RuneCountInString(str(row["Name"])) < 1 || utf8.RuneCountInString(str(row["Name"])) > 64 {
		fail(422, "Server name must contain 1 to 64 characters.")
	}
	host, port, auth := connectionInput(M{"host": row["Host"], "port": row["Port"], "auth": row["AuthType"], "password": body["password"]})
	row["Host"], row["Port"], row["AuthType"] = host, port, auth
	for _, key := range []string{"is_enabled", "is_default", "skip_cert_check"} {
		if v, ok := body[key]; ok {
			if _, valid := v.(bool); !valid {
				fail(422, key+" must be a boolean.")
			}
		}
	}
	if boolean(row["IsDefault"]) && !boolean(row["IsEnabled"]) {
		fail(422, "The default server must be enabled.")
	}
	if v, ok := body["password"]; ok && v != nil {
		if _, valid := v.(string); !valid {
			fail(422, "Password must be a string.")
		}
		row["EncryptedPassword"] = a.encrypt(str(v))
	}
	if one(a.db, `SELECT ServerID FROM Server WHERE lower(Host)=lower(?) AND Port=? AND ServerID!=?`, host, port, integer(existing["ServerID"])) != nil {
		fail(409, "A server with this address and port already exists.")
	}
	return row
}

func (a *App) requireUnassignedCertificateTarget(id int64) {
	if integer(a.setting("cert_push_server_id")) == id {
		fail(409, "Remove this server's automatic certificate deployment target before changing its address.")
	}
}

func (a *App) registerServers() {
	a.endpoint("GET /servers", func(_ http.ResponseWriter, _ *http.Request, _ M) any {
		out := []M{}
		for _, row := range query(a.db, `SELECT * FROM Server ORDER BY SortOrder,ServerID`) {
			out = append(out, publicServer(row))
		}
		return out
	})
	a.endpoint("GET /servers/{id}", func(_ http.ResponseWriter, r *http.Request, _ M) any { return publicServer(a.serverRow(serverID(r))) })
	for _, method := range []string{"POST", "PUT"} {
		path := "/servers"
		if method == "PUT" {
			path += "/{id}"
		}
		a.endpoint(method+" "+path, func(w http.ResponseWriter, r *http.Request, u M) any {
			body := readBody(w, r)
			existing := M{"Host": "127.0.0.1", "Port": 5555, "AuthType": "", "EncryptedPassword": a.encrypt(""), "IsEnabled": true, "IsDefault": false, "SkipCertCheck": false, "SortOrder": 0}
			if method == "PUT" {
				existing = a.serverRow(serverID(r))
			}
			row := a.serverInput(existing, body)
			retargeting := method == "PUT" && (!strings.EqualFold(str(existing["Host"]), str(row["Host"])) || integer(existing["Port"]) != integer(row["Port"]))
			if retargeting {
				a.requireUnassignedCertificateTarget(integer(existing["ServerID"]))
			}
			if retargeting || (method == "PUT" && boolean(existing["IsEnabled"]) && !boolean(row["IsEnabled"])) {
				a.requireReleasedQuotas(integer(existing["ServerID"]))
			}
			if !boolean(body["skip_test"]) && (method == "POST" || body["host"] != nil || body["port"] != nil || body["password"] != nil || body["auth"] != nil) {
				pwd, err := a.decrypt(str(row["EncryptedPassword"]))
				check(err)
				out := a.testConnection(r.Context(), str(row["Host"]), int(integer(row["Port"])), pwd, str(row["AuthType"]), boolean(row["SkipCertCheck"]))
				row["AuthType"] = out["auth"]
			}
			a.connectionMu.Lock()
			defer a.connectionMu.Unlock()
			// Check again after the network test: another server may now own the address.
			if one(a.db, `SELECT ServerID FROM Server WHERE lower(Host)=lower(?) AND Port=? AND ServerID!=?`, row["Host"], row["Port"], integer(existing["ServerID"])) != nil {
				fail(409, "A server with this address and port already exists.")
			}
			if method == "PUT" {
				current := a.serverRow(integer(existing["ServerID"]))
				if boolean(current["IsDefault"]) != boolean(existing["IsDefault"]) {
					fail(409, "The default server changed during the connection test. Try again.")
				}
			}
			if str(row["AuthType"]) == "" {
				row["AuthType"] = "header"
			}
			if integer(one(a.db, `SELECT COUNT(*) n FROM Server`)["n"]) == 0 {
				row["IsDefault"] = true
			}
			if boolean(existing["IsDefault"]) && !boolean(row["IsDefault"]) {
				fail(422, "Choose another default server first.")
			}
			stamp := now()
			tx, err := a.db.Begin()
			check(err)
			defer tx.Rollback()
			if boolean(row["IsDefault"]) {
				_, err = tx.Exec(`UPDATE Server SET IsDefault=0`)
				check(err)
			}
			id := integer(existing["ServerID"])
			if method == "POST" {
				var result sql.Result
				result, err = tx.Exec(`INSERT INTO Server(Name,Host,Port,EncryptedPassword,AuthType,IsEnabled,IsDefault,SkipCertCheck,SortOrder,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, row["Name"], row["Host"], row["Port"], row["EncryptedPassword"], row["AuthType"], boolean(row["IsEnabled"]), boolean(row["IsDefault"]), boolean(row["SkipCertCheck"]), integer(row["SortOrder"]), stamp, stamp)
				check(err)
				id, err = result.LastInsertId()
				check(err)
			} else {
				_, err = tx.Exec(`UPDATE Server SET Name=?,Host=?,Port=?,EncryptedPassword=?,AuthType=?,IsEnabled=?,IsDefault=?,SkipCertCheck=?,SortOrder=?,UpdatedDate=? WHERE ServerID=?`, row["Name"], row["Host"], row["Port"], row["EncryptedPassword"], row["AuthType"], boolean(row["IsEnabled"]), boolean(row["IsDefault"]), boolean(row["SkipCertCheck"]), integer(row["SortOrder"]), stamp, id)
				check(err)
			}
			check(tx.Commit())
			if retargeting {
				a.resetQuotaBaselines(id)
				execute(a.db, `UPDATE Server SET BackupLastRun='',BackupLastError='' WHERE ServerID=?`, id)
			}
			a.invalidateClientLocked(id)
			if boolean(row["IsDefault"]) {
				a.syncDefaultConnection(row)
			}
			action := "server.updated"
			if method == "POST" {
				action = "server.created"
			}
			a.audit(u, action, "server", str(row["Name"]), str(row["Host"]), id)
			if boolean(row["SkipCertCheck"]) && !boolean(existing["SkipCertCheck"]) {
				a.audit(u, "server.certificate_check_disabled", "server", str(row["Name"]), str(row["Host"]), id)
			}
			return publicServer(a.serverRow(id))
		})
	}
	a.endpoint("DELETE /servers/{id}", func(_ http.ResponseWriter, r *http.Request, u M) any {
		id := serverID(r)
		a.connectionMu.Lock()
		defer a.connectionMu.Unlock()
		row := a.serverRow(id)
		if integer(one(a.db, `SELECT COUNT(*) n FROM Server`)["n"]) <= 1 {
			fail(422, "Cannot delete the only configured server.")
		}
		if id == a.serverIDFromContext(r.Context()) {
			fail(409, "Switch to another server before removing the current connection.")
		}
		a.requireReleasedQuotas(id)
		replacement := one(a.db, `SELECT * FROM Server WHERE ServerID!=? AND IsEnabled=1 ORDER BY SortOrder,ServerID LIMIT 1`, id)
		if boolean(row["IsDefault"]) && replacement == nil {
			fail(409, "Choose an enabled default server first.")
		}
		tx, err := a.db.Begin()
		check(err)
		defer tx.Rollback()
		_, err = tx.Exec(`DELETE FROM VpnSessionTrafficSample WHERE VpnSessionSampleID IN (SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=?)`, id)
		check(err)
		for _, table := range []string{"UserTrafficSample", "HubTrafficSample", "VpnSessionSample", "VpnUserCredential", "TrafficQuota"} {
			_, err = tx.Exec(`DELETE FROM `+table+` WHERE ServerID=?`, id)
			check(err)
		}
		_, err = tx.Exec(`UPDATE AuditLog SET ServerID=NULL WHERE ServerID=?`, id)
		check(err)
		_, err = tx.Exec(`DELETE FROM Server WHERE ServerID=?`, id)
		check(err)
		if boolean(row["IsDefault"]) {
			_, err = tx.Exec(`UPDATE Server SET IsDefault=1 WHERE ServerID=?`, replacement["ServerID"])
			check(err)
		}
		check(tx.Commit())
		a.invalidateClientLocked(id)
		if boolean(row["IsDefault"]) {
			a.syncDefaultConnection(replacement)
		}
		a.audit(u, "server.deleted", "server", str(row["Name"]), str(row["Host"]))
		return M{"ok": true}
	})
	a.endpoint("POST /servers/test", func(w http.ResponseWriter, r *http.Request, _ M) any {
		body := readBody(w, r)
		host, port, auth := connectionInput(body)
		start := time.Now()
		out := a.testConnection(r.Context(), host, port, str(body["password"]), auth, boolean(body["skip_cert_check"]))
		out["rtt_ms"] = time.Since(start).Milliseconds()
		return out
	})
	a.endpoint("POST /servers/{id}/test", func(w http.ResponseWriter, r *http.Request, _ M) any {
		row := a.serverRow(serverID(r))
		body := M{}
		if r.ContentLength != 0 {
			body = readBody(w, r)
		}
		host, port, auth := connectionInput(M{"host": withDefault(body, "host", row["Host"]), "port": withDefault(body, "port", row["Port"]), "auth": withDefault(body, "auth", row["AuthType"]), "password": body["password"]})
		pwd, err := a.decrypt(str(row["EncryptedPassword"]))
		check(err)
		if v, ok := body["password"]; ok && v != nil {
			pwd = str(v)
		}
		start := time.Now()
		out := a.testConnection(r.Context(), host, port, pwd, auth, boolean(withDefault(body, "skip_cert_check", row["SkipCertCheck"])))
		out["rtt_ms"] = time.Since(start).Milliseconds()
		return out
	})
	a.endpoint("POST /servers/{id}/set-default", func(_ http.ResponseWriter, r *http.Request, u M) any {
		id := serverID(r)
		a.connectionMu.Lock()
		defer a.connectionMu.Unlock()
		row := a.serverRow(id)
		if !boolean(row["IsEnabled"]) {
			fail(422, "The default server must be enabled.")
		}
		execute(a.db, `UPDATE Server SET IsDefault=CASE WHEN ServerID=? THEN 1 ELSE 0 END`, id)
		a.syncDefaultConnection(row)
		a.audit(u, "server.set_default", "server", str(row["Name"]), str(row["Host"]), id)
		return M{"ok": true, "default_id": id}
	})
	a.endpoint("POST /servers/{id}/probe", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		row := a.serverRow(serverID(r))
		return a.probeServer(context.WithValue(r.Context(), serverIDKey, integer(row["ServerID"])))
	})
}
