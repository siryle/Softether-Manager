package manager

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) registerServers() {
	a.endpoint("GET /servers", func(_ http.ResponseWriter, _ *http.Request, _ M) any {
		rows := query(a.db, `SELECT ServerID id, Name name, Host host, Port port, AuthType auth, IsEnabled is_enabled, IsDefault is_default, SortOrder sort_order, CreatedDate created_date, UpdatedDate updated_date FROM Server ORDER BY SortOrder, ServerID`)
		for i := range rows {
			rows[i]["is_enabled"] = boolean(rows[i]["is_enabled"])
			rows[i]["is_default"] = boolean(rows[i]["is_default"])
		}
		return rows
	})

	a.endpoint("POST /servers", func(w http.ResponseWriter, r *http.Request, u M) any {
		body := readBody(w, r)
		name := strings.TrimSpace(str(body["name"]))
		if name == "" {
			fail(422, "Server name is required.")
		}
		host, port, auth := connectionInput(body)
		pwd := str(body["password"])
		if !boolean(body["skip_test"]) && pwd != "" {
			out := a.testConnection(r.Context(), host, port, pwd, auth)
			auth = str(out["auth"])
		}
		isDefault := boolean(body["is_default"])
		count := integer(one(a.db, `SELECT COUNT(*) n FROM Server`)["n"])
		if count == 0 {
			isDefault = true
		}
		if isDefault {
			execute(a.db, `UPDATE Server SET IsDefault=0`)
		}
		encPwd := a.encrypt(pwd)
		stamp := now()
		id := execute(a.db, `INSERT INTO Server(Name,Host,Port,EncryptedPassword,AuthType,IsEnabled,IsDefault,SortOrder,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,1,?,?,?,?)`, name, host, port, encPwd, auth, isDefault, 0, stamp, stamp)
		if isDefault {
			a.saveConnection(host, port, &pwd, auth)
		}
		a.audit(u, "server.created", "server", name, host)
		return M{
			"id":         id,
			"name":       name,
			"host":       host,
			"port":       port,
			"auth":       auth,
			"is_enabled": true,
			"is_default": isDefault,
		}
	})

	a.endpoint("GET /servers/{id}", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		row := one(a.db, `SELECT ServerID id, Name name, Host host, Port port, AuthType auth, IsEnabled is_enabled, IsDefault is_default, SortOrder sort_order, CreatedDate created_date, UpdatedDate updated_date FROM Server WHERE ServerID=?`, id)
		if row == nil {
			fail(404, "Server not found.")
		}
		row["is_enabled"] = boolean(row["is_enabled"])
		row["is_default"] = boolean(row["is_default"])
		return row
	})

	a.endpoint("PUT /servers/{id}", func(w http.ResponseWriter, r *http.Request, u M) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		existing := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, id)
		if existing == nil {
			fail(404, "Server not found.")
		}
		body := readBody(w, r)
		name := str(existing["Name"])
		if v, ok := body["name"]; ok && str(v) != "" {
			name = strings.TrimSpace(str(v))
		}
		host := str(existing["Host"])
		if v, ok := body["host"]; ok && str(v) != "" {
			host = strings.TrimSpace(str(v))
		}
		port := int(integer(existing["Port"]))
		if v, ok := body["port"]; ok && integer(v) > 0 {
			port = int(integer(v))
		}
		auth := str(existing["AuthType"])
		if v, ok := body["auth"]; ok && str(v) != "" {
			auth = str(v)
		}
		encPwd := str(existing["EncryptedPassword"])
		var rawPwd *string
		if v, ok := body["password"]; ok && v != nil && str(v) != "" {
			p := str(v)
			rawPwd = &p
			if !boolean(body["skip_test"]) {
				out := a.testConnection(r.Context(), host, port, p, auth)
				auth = str(out["auth"])
			}
			encPwd = a.encrypt(p)
		}
		isEnabled := boolean(existing["IsEnabled"])
		if v, ok := body["is_enabled"]; ok && v != nil {
			isEnabled = boolean(v)
		}
		isDefault := boolean(existing["IsDefault"])
		if v, ok := body["is_default"]; ok && v != nil {
			isDefault = boolean(v)
		}
		if isDefault {
			execute(a.db, `UPDATE Server SET IsDefault=0 WHERE ServerID!=?`, id)
		}
		execute(a.db, `UPDATE Server SET Name=?,Host=?,Port=?,EncryptedPassword=?,AuthType=?,IsEnabled=?,IsDefault=?,UpdatedDate=? WHERE ServerID=?`, name, host, port, encPwd, auth, isEnabled, isDefault, now(), id)
		a.invalidateClient(id)
		if isDefault {
			if rawPwd == nil {
				p, _ := a.decrypt(encPwd)
				rawPwd = &p
			}
			a.saveConnection(host, port, rawPwd, auth)
		}
		a.audit(u, "server.updated", "server", name, host)
		return M{"id": id, "name": name, "host": host, "port": port, "auth": auth, "is_enabled": isEnabled, "is_default": isDefault}
	})

	a.endpoint("DELETE /servers/{id}", func(_ http.ResponseWriter, r *http.Request, u M) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		existing := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, id)
		if existing == nil {
			fail(404, "Server not found.")
		}
		count := integer(one(a.db, `SELECT COUNT(*) n FROM Server`)["n"])
		if count <= 1 {
			fail(422, "Cannot delete the only configured server.")
		}
		wasDefault := boolean(existing["IsDefault"])
		execute(a.db, `DELETE FROM Server WHERE ServerID=?`, id)
		execute(a.db, `DELETE FROM UserTrafficSample WHERE ServerID=?`, id)
		execute(a.db, `DELETE FROM HubTrafficSample WHERE ServerID=?`, id)
		execute(a.db, `DELETE FROM VpnSessionSample WHERE ServerID=?`, id)
		execute(a.db, `DELETE FROM VpnUserCredential WHERE ServerID=?`, id)
		execute(a.db, `DELETE FROM TrafficQuota WHERE ServerID=?`, id)
		a.invalidateClient(id)
		if wasDefault {
			row := one(a.db, `SELECT ServerID FROM Server ORDER BY ServerID LIMIT 1`)
			if row != nil {
				execute(a.db, `UPDATE Server SET IsDefault=1 WHERE ServerID=?`, row["ServerID"])
			}
		}
		a.audit(u, "server.deleted", "server", str(existing["Name"]), str(existing["Host"]))
		return M{"ok": true}
	})

	a.endpoint("POST /servers/test", func(w http.ResponseWriter, r *http.Request, _ M) any {
		body := readBody(w, r)
		host, port, auth := connectionInput(body)
		password := str(body["password"])
		start := time.Now()
		out := a.testConnection(r.Context(), host, port, password, auth)
		out["rtt_ms"] = time.Since(start).Milliseconds()
		return out
	})

	a.endpoint("POST /servers/{id}/test", func(w http.ResponseWriter, r *http.Request, _ M) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		row := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, id)
		if row == nil {
			fail(404, "Server not found.")
		}
		pwd, _ := a.decrypt(str(row["EncryptedPassword"]))
		start := time.Now()
		out := a.testConnection(r.Context(), str(row["Host"]), int(integer(row["Port"])), pwd, str(row["AuthType"]))
		out["rtt_ms"] = time.Since(start).Milliseconds()
		return out
	})

	a.endpoint("POST /servers/{id}/set-default", func(_ http.ResponseWriter, r *http.Request, u M) any {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		row := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, id)
		if row == nil {
			fail(404, "Server not found.")
		}
		execute(a.db, `UPDATE Server SET IsDefault=CASE WHEN ServerID=? THEN 1 ELSE 0 END`, id)
		pwd, _ := a.decrypt(str(row["EncryptedPassword"]))
		a.saveConnection(str(row["Host"]), int(integer(row["Port"])), &pwd, str(row["AuthType"]))
		a.audit(u, "server.set_default", "server", str(row["Name"]), str(row["Host"]))
		return M{"ok": true, "default_id": id}
	})

	a.endpoint("POST /servers/{id}/probe", func(_ http.ResponseWriter, r *http.Request, _ M) (result any) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			fail(422, "Invalid server id.")
		}
		serverCtx := context.WithValue(r.Context(), serverIDKey, id)
		defer func() {
			if p := recover(); p != nil {
				result = M{"online": false, "configured": true, "error": fmtError(p)}
			}
		}()
		info := a.rpc(serverCtx, "GetServerInfo", nil)
		status := a.rpc(serverCtx, "GetServerStatus", nil)
		return M{
			"online":       true,
			"configured":   true,
			"version":      info["ServerVersionString_str"],
			"server_type":  info["ServerType_u32"],
			"hostname":     info["ServerHostName_str"],
			"sessions":     status["NumSessionsTotal_u32"],
			"users":        status["NumUsers_u32"],
			"hubs":         status["NumHubTotal_u32"],
			"send_bytes":   status["SendBytes_u64"],
			"recv_bytes":   status["RecvBytes_u64"],
			"started":      status["StartTime_dt"],
			"current_time": status["CurrentTime_dt"],
		}
	})
}
