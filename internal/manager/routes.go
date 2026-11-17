package manager

import (
	"encoding/base64"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (a *App) register() {
	a.registerAuth()
	a.registerConnection()
	a.registerServers()
	a.registerRPCTrust()
	a.registerSystem()
	a.registerRPCRoutes()
	a.registerCustomRPC()
	a.registerDDNS()
	a.registerHistory()
	a.registerVPN()
	a.registerQuotas()
	a.registerBackups()
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			fail(405, "Method not allowed.")
		}
		if out := a.static(w, r, nil); out != nil {
			writeJSON(w, 200, out)
		}
	})
}
func (a *App) endpoint(pattern string, fn func(http.ResponseWriter, *http.Request, M) any) {
	parts := strings.SplitN(pattern, " ", 2)
	a.handle(parts[0]+" /api/v1"+parts[1], false, fn)
}
func (a *App) registerCustomRPC() {
	a.endpoint("GET /server/overview", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out := M{}
		for key, method := range map[string]string{"info": "GetServerInfo", "status": "GetServerStatus", "hubs": "EnumHub", "listeners": "EnumListener", "connections": "EnumConnection", "ipsec": "GetIPsecServices", "openvpn": "GetOpenVpnSstpConfig", "azure": "GetAzureStatus", "ddns": "GetDDnsClientStatus", "bridge_support": "GetBridgeSupport"} {
			func() {
				defer func() {
					if p := recover(); p != nil {
						out[key] = M{"ok": false, "error": fmtError(p)}
					}
				}()
				out[key] = M{"ok": true, "data": a.rpc(r.Context(), method, nil)}
			}()
		}
		return out
	})
	a.endpoint("PUT /server/listeners/{port}", func(w http.ResponseWriter, r *http.Request, u M) any {
		port, err := strconv.ParseUint(r.PathValue("port"), 10, 16)
		if err != nil || port == 0 {
			fail(422, "Invalid listener port.")
		}
		b := readBody(w, r)
		enabled := boolean(withDefault(b, "enable", true))
		a.rpc(r.Context(), "EnableListener", M{"Port_u32": port, "Enable_bool": enabled})
		all := a.rpc(r.Context(), "EnumListener", nil)["ListenerList"]
		for _, entry := range list(all) {
			if integer(entry["Ports_u32"]) == int64(port) {
				out := clone(entry)
				out["enabled"] = boolean(entry["Enables_bool"])
				out["ListenerList"] = all
				a.audit(u, "listener.toggled", "server", "", str(port))
				return out
			}
		}
		fail(404, "No listener on this port.")
		return nil
	})
	a.endpoint("GET /server/cert", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		return M{"cert_base64": a.rpc(r.Context(), "GetServerCert", nil)["Cert_bin"]}
	})
	a.endpoint("PUT /server/admin-password", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		p, ok := b["password"].(string)
		if !ok {
			fail(422, "Password is required.")
		}
		out := a.rpc(r.Context(), "SetServerPassword", M{"PlainTextPassword_str": p})
		conn := a.connection(r.Context())
		a.saveConnection(str(conn["host"]), int(integer(conn["port"])), &p, "", r.Context())
		a.audit(u, "server.admin_password_changed", "server", "", "")
		return out
	})
	for _, action := range []string{"start", "stop"} {
		a.endpoint("POST /server/l3/{name}/"+action, func(_ http.ResponseWriter, r *http.Request, u M) any {
			method := "StartL3Switch"
			if action == "stop" {
				method = "StopL3Switch"
			}
			name := r.PathValue("name")
			a.rpc(r.Context(), method, M{"Name_str": name})
			a.audit(u, "l3."+action, "server", name, "")
			for _, entry := range list(a.rpc(r.Context(), "EnumL3Switch", nil)["L3SWList"]) {
				if str(entry["Name_str"]) == name {
					entry["active"] = boolean(entry["Active_bool"])
					return entry
				}
			}
			fail(404, "No such layer-3 switch.")
			return nil
		})
	}
	a.endpoint("GET /server/logs/read", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		path := r.URL.Query().Get("path")
		if path == "" {
			fail(422, "A log path is required.")
		}
		offset := queryInt(r, "offset", 0, 0, 1<<32-1)
		p := M{"FilePath_str": path}
		if offset != 0 {
			p["Offset_u32"] = offset
		}
		out := a.rpc(r.Context(), "ReadLogFile", p)
		raw, err := base64.StdEncoding.DecodeString(str(out["Buffer_bin"]))
		if err != nil {
			fail(502, "SoftEther returned an invalid log buffer.")
		}
		return M{"path": path, "offset": offset, "text": strings.ToValidUTF8(string(raw), "�"), "bytes": len(raw)}
	})
	for _, path := range []string{"/server/config", "/server/openvpn/sample-config"} {
		method := "GET"
		if strings.Contains(path, "sample-config") {
			method = "POST"
		}
		a.endpoint(method+" "+path, func(_ http.ResponseWriter, r *http.Request, _ M) any {
			if method == "POST" {
				out := a.rpc(r.Context(), "MakeOpenVpnConfigFile", nil)
				return M{"filename": withDefault(out, "FileName_str", "openvpn_config.zip"), "zip_base64": withDefault(out, "Buffer_bin", "")}
			}
			out := a.rpc(r.Context(), "GetConfig", nil)
			return M{"filename": withDefault(out, "FileName_str", "vpn_server.config"), "config_base64": withDefault(out, "FileData_bin", "")}
		})
	}
	a.endpoint("GET /server/default-hub-admin-options", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		return a.rpc(r.Context(), "GetDefaultHubAdminOptions", M{"HubName_str": r.URL.Query().Get("hub")})
	})
	a.endpoint("DELETE /hubs/{hub}", func(_ http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		out := a.rpc(r.Context(), "DeleteHub", M{"HubName_str": hub})
		serverID := a.serverIDFromContext(r.Context())
		execute(a.db, `DELETE FROM TrafficQuota WHERE ServerID=? AND HubName=?`, serverID, hub)
		execute(a.db, `DELETE FROM VpnUserCredential WHERE ServerID=? AND HubName=?`, serverID, hub)
		a.audit(u, "hub.deleted", "hub", hub, "")
		return out
	})
	a.endpoint("PUT /hubs/{hub}/online", func(w http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		b := readBody(w, r)
		on, ok := b["online"].(bool)
		if !ok {
			fail(422, "online must be a boolean.")
		}
		a.guardHubOnline(r.Context(), "SetHubOnline", M{"HubName_str": hub, "Online_bool": on})
		a.rpc(r.Context(), "SetHubOnline", M{"HubName_str": hub, "Online_bool": on})
		out := a.rpc(r.Context(), "GetHubStatus", M{"HubName_str": hub})
		out["online"] = boolean(out["Online_bool"])
		a.audit(u, "hub.online_toggled", "hub", hub, "")
		return out
	})
	a.endpoint("GET /hubs/{hub}/message", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out := a.rpc(r.Context(), "GetHubMsg", M{"HubName_str": r.PathValue("hub")})
		raw, _ := base64.StdEncoding.DecodeString(str(out["Msg_bin"]))
		return M{"message": strings.ToValidUTF8(string(raw), "�")}
	})
	a.endpoint("PUT /hubs/{hub}/message", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		message, ok := b["message"].(string)
		if !ok {
			fail(422, "message is required.")
		}
		hub := r.PathValue("hub")
		out := a.rpc(r.Context(), "SetHubMsg", M{"HubName_str": hub, "Msg_bin": base64.StdEncoding.EncodeToString([]byte(message))})
		a.audit(u, "hub.message_updated", "hub", hub, "")
		return out
	})
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		path := "/hubs/{hub}/users"
		if method != "POST" {
			path += "/{name}"
		}
		a.endpoint(method+" "+path, func(w http.ResponseWriter, r *http.Request, u M) any {
			hub, name := r.PathValue("hub"), r.PathValue("name")
			body := M{}
			rpcMethod := "DeleteUser"
			if method != "DELETE" {
				body = readBody(w, r)
				rpcMethod = "SetUser"
				if method == "POST" {
					rpcMethod = "CreateUser"
					name = str(body["Name_str"])
				}
			}
			body["HubName_str"], body["Name_str"] = hub, name
			validateWire(body)
			out := a.rpc(r.Context(), rpcMethod, body)
			serverID := a.serverIDFromContext(r.Context())
			if method == "DELETE" {
				execute(a.db, `DELETE FROM VpnUserCredential WHERE ServerID=? AND HubName=? AND UserName=?`, serverID, hub, name)
				execute(a.db, `DELETE FROM TrafficQuota WHERE ServerID=? AND SubjectType='user' AND HubName=? AND UserKey=?`, serverID, hub, fold(name))
			} else if password := str(body["Auth_Password_str"]); password != "" && integer(withDefault(body, "AuthType_u32", 1)) == 1 {
				a.saveCredential(serverID, hub, name, hashedPassword(password, name))
			}
			a.audit(u, "user."+strings.ToLower(method), "vpn_user", name, "hub "+hub)
			return out
		})
	}
	for _, path := range []string{"access", "ac-list"} {
		for _, method := range []string{"POST", "PUT"} {
			if path == "ac-list" && method == "POST" {
				continue
			}
			a.endpoint(method+" /hubs/{hub}/"+path, func(w http.ResponseWriter, r *http.Request, u M) any {
				b := readBody(w, r)
				a.accessMu.Lock()
				defer a.accessMu.Unlock()
				p := M{"HubName_str": r.PathValue("hub")}
				rpcMethod, key := "SetAccessList", "AccessList"
				if path == "ac-list" {
					rpcMethod, key = "SetAcList", "ACList"
				}
				if method == "POST" {
					rpcMethod = "AddAccess"
					p["AccessListSingle"] = []M{b}
				} else {
					if _, ok := b[key].([]any); !ok {
						fail(422, "Body must carry an "+key+" array.")
					}
					p[key] = b[key]
					a.checkAccessVersion(r.Context(), r.PathValue("hub"), key, str(b["expected_version"]))
				}
				out := a.rpc(r.Context(), rpcMethod, p)
				a.audit(u, "access.updated", "hub", r.PathValue("hub"), "")
				return out
			})
		}
	}
	for _, action := range []string{"overview", "enable", "disable", "options"} {
		pattern := "GET /hubs/{hub}/securenat"
		if action == "enable" || action == "disable" {
			pattern = "POST /hubs/{hub}/securenat/" + action
		}
		if action == "options" {
			pattern = "PUT /hubs/{hub}/securenat/options"
		}
		a.endpoint(pattern, func(w http.ResponseWriter, r *http.Request, u M) any {
			hub := r.PathValue("hub")
			p := M{"HubName_str": hub}
			if action == "enable" || action == "disable" {
				method := "EnableSecureNAT"
				if action == "disable" {
					method = "DisableSecureNAT"
				}
				a.rpc(r.Context(), method, p)
				a.audit(u, "securenat."+action, "hub", hub, "")
			}
			if action == "options" {
				b := readBody(w, r)
				current := a.rpc(r.Context(), "GetSecureNATOption", M{"RpcHubName_str": hub})
				for k, v := range b {
					current[k] = v
				}
				current["RpcHubName_str"] = hub
				out := a.rpc(r.Context(), "SetSecureNATOption", current)
				a.audit(u, "securenat.options_updated", "hub", hub, "")
				return out
			}
			status := a.rpc(r.Context(), "GetHubStatus", p)
			return M{"enabled": boolean(status["SecureNATEnabled_bool"]), "hub_online": boolean(status["Online_bool"]), "status": a.rpc(r.Context(), "GetSecureNATStatus", p), "options": a.rpc(r.Context(), "GetSecureNATOption", M{"RpcHubName_str": hub})}
		})
	}
	a.endpoint("PUT /hubs/{hub}/links/{name}/online", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		on, ok := b["online"].(bool)
		if !ok {
			fail(422, "online must be a boolean.")
		}
		hub, name := r.PathValue("hub"), r.PathValue("name")
		method := "SetLinkOffline"
		if on {
			method = "SetLinkOnline"
		}
		a.rpc(r.Context(), method, M{"HubName_str": hub, "AccountName_utf": name})
		a.audit(u, "link.online_toggled", "hub", hub, name)
		for _, entry := range list(a.rpc(r.Context(), "EnumLink", M{"HubName_str": hub})["LinkList"]) {
			if str(entry["AccountName_utf"]) == name {
				entry["online"] = boolean(entry["Online_bool"])
				return entry
			}
		}
		fail(404, "No such cascade connection.")
		return nil
	})
	a.endpoint("GET /hubs/{hub}/users-online", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		names := []string{}
		for name := range a.onlineUsers(r, r.PathValue("hub")) {
			names = append(names, name)
		}
		sort.Strings(names)
		return M{"HubName_str": r.PathValue("hub"), "usernames": names}
	})
	a.endpoint("GET /hubs/{hub}/sessions", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		hub := r.PathValue("hub")
		sessions := list(a.rpc(r.Context(), "EnumSession", M{"HubName_str": hub})["SessionList"])
		return M{"HubName_str": hub, "SessionList": a.enrichSessions(r.Context(), hub, sessions)}
	})
	a.endpoint("GET /hubs/{hub}/sessions/{name...}", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		hub, name := r.PathValue("hub"), r.PathValue("name")
		status := a.rpc(r.Context(), "GetSessionStatus", M{"HubName_str": hub, "Name_str": name})
		return a.enrichSessionStatus(r.Context(), hub, name, status)
	})
	a.endpoint("GET /hubs/{hub}/users/{name}/sessions", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		hub, name := r.PathValue("hub"), r.PathValue("name")
		out := []M{}
		for _, s := range list(a.rpc(r.Context(), "EnumSession", M{"HubName_str": hub})["SessionList"]) {
			if fold(str(s["Username_str"])) == fold(name) {
				out = append(out, s)
			}
		}
		return M{"HubName_str": hub, "SessionList": a.enrichSessions(r.Context(), hub, out)}
	})
	a.endpoint("GET /users", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		hubs, users, errors := []string{}, []M{}, []M{}
		for _, h := range list(a.rpc(r.Context(), "EnumHub", nil)["HubList"]) {
			hub := str(h["HubName_str"])
			if hub == "" {
				continue
			}
			hubs = append(hubs, hub)
			func() {
				defer func() {
					if p := recover(); p != nil {
						errors = append(errors, M{"hub": hub, "error": fmtError(p)})
					}
				}()
				online := a.onlineUsers(r, hub)
				for _, u := range list(a.rpc(r.Context(), "EnumUser", M{"HubName_str": hub})["UserList"]) {
					u["HubName_str"] = hub
					u["Online_bool"] = online[fold(str(u["Name_str"]))]
					users = append(users, u)
				}
			}()
		}
		return M{"hubs": hubs, "users": users, "errors": errors}
	})
	a.endpoint("GET /rpc/methods", func(_ http.ResponseWriter, _ *http.Request, _ M) any { return documentedMethods() })
	a.endpoint("POST /rpc/call", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		method := str(b["method"])
		found := false
		for _, m := range documentedMethods() {
			if str(m["name"]) == method {
				found = true
				break
			}
		}
		if !found {
			fail(422, method+" is not a documented RPC method.")
		}
		params := object(b["params"])
		validateWire(params)
		a.guardHubOnline(r.Context(), method, params)
		out := a.rpc(r.Context(), method, params)
		a.audit(u, "rpc.called", "server", "", method)
		return out
	})
}
func (a *App) onlineUsers(r *http.Request, hub string) (out map[string]bool) {
	out = map[string]bool{}
	defer func() { _ = recover() }()
	for _, s := range list(a.rpc(r.Context(), "EnumSession", M{"HubName_str": hub})["SessionList"]) {
		if boolean(s["SecureNATMode_bool"]) || boolean(s["BridgeMode_bool"]) || boolean(s["LinkMode_bool"]) {
			continue
		}
		if name := fold(strings.TrimSpace(str(s["Username_str"]))); name != "" {
			out[name] = true
		}
	}
	return out
}
func queryInt(r *http.Request, key string, fallback, low, high int64) int64 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		fail(422, "Invalid integer query: "+key)
	}
	return max(low, min(high, n))
}
