package manager

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (a *App) register() {
	a.registerAuth()
	a.registerConnection()
	a.registerServers()
	a.registerRPCTrust()
	a.registerSystem()
	a.registerRPCRoutes()
	a.registerCustomRPC()
	a.registerUserBulk()
	a.registerDDNS()
	a.registerWireGuard()
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
func (a *App) mergeUserUpdate(ctx context.Context, hub, name string, changes M) M {
	current := userWriteBody(a.rpc(ctx, "GetUser", M{"HubName_str": hub, "Name_str": name}))
	oldAuth := integer(current["AuthType_u32"])
	auth := integer(withDefault(changes, "AuthType_u32", oldAuth))
	if auth != oldAuth {
		if (auth == 1 && str(changes["Auth_Password_str"]) == "") || (auth == 2 && str(changes["UserX_bin"]) == "") {
			fail(422, "Changing the authentication type requires a new password or certificate.")
		}
		for _, key := range []string{"HashedKey_bin", "NtLmSecureHash_bin", "UserX_bin", "Serial_bin", "CommonName_utf", "RadiusUsername_utf", "NtUsername_utf"} {
			delete(current, key)
		}
	}
	for key, value := range userWriteBody(changes) {
		if key != "HashedKey_bin" && key != "NtLmSecureHash_bin" {
			current[key] = value
		}
	}
	if password, ok := changes["Auth_Password_str"]; ok {
		if _, valid := password.(string); !valid || str(password) == "" || auth != 1 {
			fail(422, "A new password must be a non-empty string for a password-authenticated user.")
		}
		current["Auth_Password_str"] = password
	}
	return current
}

func (a *App) registerCustomRPC() {
	a.endpoint("GET /server/overview", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		ctx := r.Context()
		serverID := a.serverIDFromContext(ctx)
		return a.singleflight("overview:"+strconv.FormatInt(serverID, 10), func() any {
			methods := map[string]string{"info": "GetServerInfo", "status": "GetServerStatus", "hubs": "EnumHub", "listeners": "EnumListener", "connections": "EnumConnection", "ipsec": "GetIPsecServices", "openvpn": "GetOpenVpnSstpConfig", "azure": "GetAzureStatus", "ddns": "GetDDnsClientStatus", "bridge_support": "GetBridgeSupport"}
			out := M{}
			var mu sync.Mutex
			var wg sync.WaitGroup
			for key, method := range methods {
				wg.Add(1)
				go func(k, m string) {
					defer wg.Done()
					var entry M
					func() {
						defer func() {
							if p := recover(); p != nil {
								entry = M{"ok": false, "error": fmtError(p)}
							}
						}()
						res := a.rpc(ctx, m, nil)
						if k == "openvpn" {
							res = a.enrichOpenVPNConfig(serverID, res)
						}
						entry = M{"ok": true, "data": res}
					}()
					mu.Lock()
					out[k] = entry
					mu.Unlock()
				}(key, method)
			}
			wg.Wait()
			if hubEntry, ok := out["hubs"].(M); ok && boolean(hubEntry["ok"]) {
				if hubData, ok := hubEntry["data"].(M); ok {
					stats := a.enrichHubListStats(ctx, list(hubData["HubList"]), true, true)
					if statusEntry, ok := out["status"].(M); ok && boolean(statusEntry["ok"]) {
						if statusData, ok := statusEntry["data"].(M); ok {
							if stats.sessionsOK {
								statusData["ClientSessionsTotal_u32"] = stats.clientSessions
							}
							if stats.trafficOK {
								statusData["ClientSendBytes_u64"] = stats.sendBytes
								statusData["ClientRecvBytes_u64"] = stats.recvBytes
							}
							if stats.rateOK {
								statusData["ClientSendBytesPerSec_f64"] = stats.sendRate
								statusData["ClientRecvBytesPerSec_f64"] = stats.recvRate
							}
						}
					}
				}
			}
			return out
		})
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
	a.endpoint("GET /server/connections", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		raw := a.rpc(r.Context(), "EnumConnection", nil)
		connections := list(raw["ConnectionList"])
		return a.enrichConnections(r.Context(), connections)
	})
	a.endpoint("GET /server/connections/{name}", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		name := r.PathValue("name")
		status := a.rpc(r.Context(), "GetConnectionInfo", M{"Name_str": name})
		if status == nil {
			fail(404, "Connection not found.")
		}
		ip := cleanIP(str(status["Ip_ip"]))
		geo := a.lookupIP(r.Context(), ip)
		status["IpLocation_str"] = geo.Location
		status["IpIsp_str"] = geo.ISP
		status["IpFull_str"] = geo.Full
		return status
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
		if server := strings.TrimSpace(r.URL.Query().Get("server")); server != "" {
			p["ServerName_str"] = server
		}
		out := a.rpc(r.Context(), "ReadLogFile", p)
		raw, err := base64.StdEncoding.DecodeString(str(out["Buffer_bin"]))
		if err != nil {
			fail(502, "SoftEther returned an invalid log buffer.")
		}
		return M{"path": path, "offset": offset, "text": strings.ToValidUTF8(string(raw), "�"), "bytes": len(raw)}
	})
	a.endpoint("GET /server/config", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out := a.rpc(r.Context(), "GetConfig", nil)
		return M{"filename": withDefault(out, "FileName_str", "vpn_server.config"), "config_base64": withDefault(out, "FileData_bin", "")}
	})
	a.endpoint("GET /server/openvpn", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out := a.rpc(r.Context(), "GetOpenVpnSstpConfig", nil)
		return a.enrichOpenVPNConfig(a.serverIDFromContext(r.Context()), out)
	})
	a.endpoint("PUT /server/openvpn", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		serverID := a.serverIDFromContext(r.Context())
		rawPortStr := strings.TrimSpace(str(b["OpenVPNPortList_str"]))
		var parsedPorts []int
		if rawPortStr != "" {
			var err error
			parsedPorts, err = parseOpenVPNPortList(rawPortStr)
			if err != nil {
				fail(422, err.Error())
			}
		} else {
			parsedPorts = []int{1194}
		}
		formattedPorts := formatPortListStr(parsedPorts)
		b["OpenVPNPortList_str"] = formattedPorts

		a.rpc(r.Context(), "SetOpenVpnSstpConfig", b)
		out := a.rpc(r.Context(), "GetOpenVpnSstpConfig", nil)

		// SoftEther 5.x ignores OpenVPNPortList_str in SetOpenVpnSstpConfig and
		// returns "" from GetOpenVpnSstpConfig, using ServerConfiguration.PortsUDP instead.
		if strings.TrimSpace(str(out["OpenVPNPortList_str"])) == "" {
			prevSaved, _ := parseOpenVPNPortList(a.savedOpenVPNPorts(serverID))
			a.syncOpenVPNUDPPorts(r.Context(), serverID, prevSaved, parsedPorts)
			a.setOpenVPNPorts(serverID, formattedPorts)
			out["OpenVPNPortList_str"] = formattedPorts
		}
		a.audit(u, "openvpn.updated", "server", "", formattedPorts)
		return out
	})
	a.endpoint("POST /server/openvpn/sample-config", func(w http.ResponseWriter, r *http.Request, _ M) any {
		var body M
		if r.Body != nil {
			rawBody, err := io.ReadAll(io.LimitReader(wMaxBytes(w, r), 64<<10))
			check(err)
			if len(bytes.TrimSpace(rawBody)) > 0 {
				d := json.NewDecoder(bytes.NewReader(rawBody))
				d.UseNumber()
				if err := d.Decode(&body); err != nil || body == nil {
					fail(422, "A JSON object body is required.")
				}
			}
		}
		serverID := a.serverIDFromContext(r.Context())
		host := strings.TrimSpace(str(body["host"]))
		if host == "" {
			host = strings.TrimSpace(str(a.connection(r.Context())["public_host"]))
		}
		port := int(integer(body["port"]))
		if port < 0 || port > 65535 {
			port = 0
		}
		if port == 0 {
			if saved := a.savedOpenVPNPorts(serverID); saved != "" {
				if ports, err := parseOpenVPNPortList(saved); err == nil && len(ports) > 0 {
					port = ports[0]
				}
			}
		}
		out := a.rpc(r.Context(), "MakeOpenVpnConfigFile", nil)
		raw, err := base64.StdEncoding.DecodeString(str(out["Buffer_bin"]))
		check(err)
		name, config, err := openvpnTemplate(raw)
		if err != nil {
			fail(502, "SoftEther returned no OpenVPN template: "+err.Error())
		}
		config = applyOpenVPNEndpoint(config, host, port)
		config = a.applyOpenVPNCA(r.Context(), config)
		config = applyOpenVPNClientDefaults(config)
		return M{"filename": name, "config": config}
	})
	a.endpoint("GET /server/default-hub-admin-options", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		return a.rpc(r.Context(), "GetDefaultHubAdminOptions", M{"HubName_str": r.URL.Query().Get("hub")})
	})
	a.endpoint("DELETE /hubs/{hub}", func(_ http.ResponseWriter, r *http.Request, u M) any {
		hub := r.PathValue("hub")
		out := a.rpc(r.Context(), "DeleteHub", M{"HubName_str": hub})
		serverID := a.serverIDFromContext(r.Context())
		execute(a.db, `DELETE FROM TrafficQuota WHERE ServerID=? AND HubName=?`, serverID, hub)
		execute(a.db, `DELETE FROM VpnUserCredential WHERE ServerID=? AND HubName=?`, serverID, hub)
		execute(a.db, `DELETE FROM WireGuardPeer WHERE ServerID=? AND HubName=?`, serverID, hub)
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
			if method == "PUT" {
				body = a.mergeUserUpdate(r.Context(), hub, name, body)
			}
			var out M
			if method == "POST" {
				out = a.createUser(r.Context(), body)
			} else {
				out = a.rpc(r.Context(), rpcMethod, body)
			}
			serverID := a.serverIDFromContext(r.Context())
			if method == "DELETE" {
				execute(a.db, `DELETE FROM VpnUserCredential WHERE ServerID=? AND HubName=? AND UserName=?`, serverID, hub, name)
				execute(a.db, `DELETE FROM TrafficQuota WHERE ServerID=? AND SubjectType='user' AND HubName=? AND UserKey=?`, serverID, hub, fold(name))
				execute(a.db, `DELETE FROM WireGuardPeer WHERE ServerID=? AND HubName=? AND UserName=?`, serverID, hub, name)
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
		serverID := a.serverIDFromContext(r.Context())
		return a.singleflight("sessions:"+strconv.FormatInt(serverID, 10)+":"+hub, func() any {
			sessions := list(a.rpc(r.Context(), "EnumSession", M{"HubName_str": hub})["SessionList"])
			return M{"HubName_str": hub, "SessionList": a.enrichSessions(r.Context(), hub, sessions)}
		})
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
			if isClientSessionRPC(s) && fold(str(s["Username_str"])) == fold(name) {
				out = append(out, s)
			}
		}
		return M{"HubName_str": hub, "SessionList": a.enrichSessions(r.Context(), hub, out)}
	})
	a.endpoint("GET /users", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		return a.singleflight("users:"+strconv.FormatInt(serverID, 10), func() any {
			hubList := list(a.rpc(r.Context(), "EnumHub", nil)["HubList"])
			hubs := make([]string, 0, len(hubList))
			for _, h := range hubList {
				if hub := str(h["HubName_str"]); hub != "" {
					hubs = append(hubs, hub)
				}
			}
			perHubUsers := make([][]M, len(hubs))
			perHubErrors := make([]M, len(hubs))
			sem := make(chan struct{}, 6)
			var wg sync.WaitGroup
			for i, hub := range hubs {
				wg.Add(1)
				go func(idx int, hubName string) {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					defer func() {
						if p := recover(); p != nil {
							perHubErrors[idx] = M{"hub": hubName, "error": fmtError(p)}
						}
					}()
					var online map[string]bool
					var userList []M
					var innerWG sync.WaitGroup
					innerWG.Add(2)
					go func() {
						defer innerWG.Done()
						online = a.onlineUsers(r, hubName)
					}()
					var enumErr any
					go func() {
						defer innerWG.Done()
						defer func() {
							if p := recover(); p != nil {
								enumErr = p
							}
						}()
						userList = list(a.rpc(r.Context(), "EnumUser", M{"HubName_str": hubName})["UserList"])
					}()
					innerWG.Wait()
					if enumErr != nil {
						panic(enumErr)
					}
					out := make([]M, 0, len(userList))
					for _, u := range userList {
						u["HubName_str"] = hubName
						u["Online_bool"] = online[fold(str(u["Name_str"]))]
						out = append(out, u)
					}
					perHubUsers[idx] = out
				}(i, hub)
			}
			wg.Wait()
			users, errors := []M{}, []M{}
			for i := range hubs {
				if perHubErrors[i] != nil {
					errors = append(errors, perHubErrors[i])
					continue
				}
				users = append(users, perHubUsers[i]...)
			}
			return M{"hubs": hubs, "users": users, "errors": errors}
		})
	})
	a.endpoint("GET /clients", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		return a.singleflight("clients:"+strconv.FormatInt(serverID, 10), func() any {
			hubList := list(a.rpc(r.Context(), "EnumHub", nil)["HubList"])
			hubs := make([]string, 0, len(hubList))
			for _, h := range hubList {
				if hub := str(h["HubName_str"]); hub != "" {
					hubs = append(hubs, hub)
				}
			}
			clients, errors := []M{}, []M{}
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, hub := range hubs {
				wg.Add(1)
				go func(hubName string) {
					defer wg.Done()
					defer func() {
						if p := recover(); p != nil {
							mu.Lock()
							errors = append(errors, M{"hub": hubName, "error": fmtError(p)})
							mu.Unlock()
						}
					}()
					sessList := list(a.rpc(r.Context(), "EnumSession", M{"HubName_str": hubName})["SessionList"])
					clientSess := []M{}
					for _, s := range sessList {
						if isClientSessionRPC(s) {
							s["HubName_str"] = hubName
							clientSess = append(clientSess, s)
						}
					}
					enriched := a.enrichSessions(r.Context(), hubName, clientSess)
					mu.Lock()
					clients = append(clients, enriched...)
					mu.Unlock()
				}(hub)
			}
			wg.Wait()
			return M{"hubs": hubs, "clients": clients, "errors": errors}
		})
	})
}
func (a *App) onlineUsers(r *http.Request, hub string) (out map[string]bool) {
	out = map[string]bool{}
	defer func() { _ = recover() }()
	ctx := r.Context()
	serverID := a.serverIDFromContext(ctx)
	wgTimeout := a.wireguardIdleTimeout()
	nowUTC := time.Now().UTC()
	for _, s := range list(a.rpc(ctx, "EnumSession", M{"HubName_str": hub})["SessionList"]) {
		if !isClientSessionRPC(s) {
			continue
		}
		if wgTimeout > 0 && isWireGuardSession(s, nil) {
			if a.isWireGuardReaped(serverID, hub, s) {
				continue
			}
			sessName := strings.TrimSpace(str(s["Name_str"]))
			var st M
			if sessName != "" {
				func() {
					defer func() { _ = recover() }()
					st = a.rpc(ctx, "GetSessionStatus", M{"HubName_str": hub, "Name_str": sessName})
				}()
			}
			if stale, lastAct := a.observeWireGuardSession(serverID, hub, s, st, nowUTC); stale {
				go a.reapWireGuardSession(ctx, serverID, hub, s, lastAct)
				continue
			}
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

func (a *App) enrichConnections(ctx context.Context, connections []M) M {
	if len(connections) == 0 {
		return M{"NumConnection_u32": 0, "ConnectionList": connections}
	}

	ips := make([]string, 0, len(connections))
	for _, c := range connections {
		if ip := str(c["Ip_ip"]); ip != "" {
			ips = append(ips, ip)
		}
	}
	geoMap := a.batchLookupIPs(ctx, ips)

	type sessionInfo struct {
		hub         string
		username    string
		sessionName string
		realname    string
		note        string
		group       string
	}
	ipToSession := make(map[string]sessionInfo)

	func() {
		defer func() { _ = recover() }()
		hubList := list(a.rpc(ctx, "EnumHub", nil)["HubList"])
		for _, h := range hubList {
			hubName := str(h["HubName_str"])
			if hubName == "" {
				continue
			}

			userMap := make(map[string]M)
			func() {
				defer func() { _ = recover() }()
				uRes := a.rpc(ctx, "EnumUser", M{"HubName_str": hubName})
				for _, u := range list(uRes["UserList"]) {
					uname := strings.ToLower(strings.TrimSpace(str(u["Name_str"])))
					if uname != "" {
						userMap[uname] = u
					}
				}
			}()

			sessList := list(a.rpc(ctx, "EnumSession", M{"HubName_str": hubName})["SessionList"])
			for _, s := range sessList {
				sessName := str(s["Name_str"])
				userName := str(s["Username_str"])
				clientIP := cleanIP(str(s["ClientIP_ip"]))
				if sessName == "" || userName == "" {
					continue
				}

				info := sessionInfo{
					hub:         hubName,
					username:    userName,
					sessionName: sessName,
				}
				if u, ok := userMap[strings.ToLower(strings.TrimSpace(userName))]; ok {
					info.realname = str(u["Realname_utf"])
					info.note = str(u["Note_utf"])
					info.group = str(u["GroupName_str"])
				}

				if clientIP != "" {
					ipToSession[clientIP] = info
				}
			}
		}
	}()

	for _, c := range connections {
		ip := cleanIP(str(c["Ip_ip"]))

		if geo, ok := geoMap[ip]; ok {
			c["IpLocation_str"] = geo.Location
			c["IpIsp_str"] = geo.ISP
			c["IpFull_str"] = geo.Full
		}

		if matched, ok := ipToSession[ip]; ok && ip != "" {
			c["Username_str"] = matched.username
			c["Realname_utf"] = matched.realname
			c["Note_utf"] = matched.note
			c["GroupName_str"] = matched.group
			c["HubName_str"] = matched.hub
			c["SessionName_str"] = matched.sessionName
		}
	}

	return M{"NumConnection_u32": len(connections), "ConnectionList": connections}
}

// SoftEther hands the sample OpenVPN configuration out as a zip holding two
// .ovpn files -- a tun (L3) remote-access client and a tap (L2) site-to-site
// bridge -- plus a readme. The panel serves the remote-access one as a
// plain-text template; the CA is already inlined by SoftEther.
func openvpnTemplate(buf []byte) (string, string, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return "", "", err
	}
	score := func(name string) int {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "remote_access") || strings.Contains(lower, "_l3") {
			return 2
		}
		return 1
	}
	var chosen *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(path.Ext(f.Name), ".ovpn") {
			continue
		}
		if chosen == nil || score(f.Name) > score(chosen.Name) {
			chosen = f
		}
	}
	if chosen == nil {
		return "", "", errors.New("the archive carries no .ovpn file")
	}
	rc, err := chosen.Open()
	if err != nil {
		return "", "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
	if err != nil {
		return "", "", err
	}
	return path.Base(chosen.Name), strings.ToValidUTF8(string(data), ""), nil
}

func wMaxBytes(w http.ResponseWriter, r *http.Request) io.Reader {
	return http.MaxBytesReader(w, r.Body, 64<<10)
}

// applyOpenVPNEndpoint rewrites active `remote <host> <port> ...` lines in a
// SoftEther .ovpn template when a public domain and/or port is configured,
// leaving commented examples and trailing protocol tokens intact.
func applyOpenVPNEndpoint(config, host string, port int) string {
	host = strings.TrimSpace(host)
	if strings.ContainsAny(host, " \t\r\n") {
		host = ""
	}
	hasPort := port >= 1 && port <= 65535
	if host == "" && !hasPort {
		return config
	}
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		cr := ""
		if strings.HasSuffix(line, "\r") {
			cr = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "remote") {
			continue
		}
		lead := line[:len(line)-len(trimmed)]
		nextHost := fields[1]
		if host != "" {
			nextHost = host
		}
		nextPort := ""
		if len(fields) >= 3 {
			nextPort = fields[2]
		}
		if hasPort {
			nextPort = strconv.Itoa(port)
		}
		parts := []string{fields[0], nextHost}
		if nextPort != "" {
			parts = append(parts, nextPort)
		}
		if len(fields) > 3 {
			parts = append(parts, fields[3:]...)
		}
		lines[i] = lead + strings.Join(parts, " ") + cr
	}
	return strings.Join(lines, "\n")
}

func parseOpenVPNPortList(s string) ([]int, error) {
	tokens := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
	if len(tokens) == 0 {
		return nil, errors.New("at least one OpenVPN UDP port is required")
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(tokens))
	for _, tok := range tokens {
		p, err := strconv.Atoi(tok)
		if err != nil || p < 1 || p > 65535 {
			return nil, errors.New("OpenVPN UDP ports must be numbers between 1 and 65535")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func formatPortListStr(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}

func (a *App) savedOpenVPNPorts(serverID int64) string {
	raw := a.setting("openvpn_ports")
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(str(m[strconv.FormatInt(serverID, 10)]))
}

func (a *App) setOpenVPNPorts(serverID int64, ports string) {
	next := M{}
	if existing, ok := a.setting("openvpn_ports").(map[string]any); ok {
		for k, v := range existing {
			next[k] = v
		}
	}
	next[strconv.FormatInt(serverID, 10)] = ports
	a.set("openvpn_ports", next)
}

func (a *App) enrichOpenVPNConfig(serverID int64, out M) M {
	if out == nil {
		return nil
	}
	if strings.TrimSpace(str(out["OpenVPNPortList_str"])) == "" {
		if saved := a.savedOpenVPNPorts(serverID); saved != "" {
			out["OpenVPNPortList_str"] = saved
		} else {
			out["OpenVPNPortList_str"] = "1194"
		}
	}
	return out
}

func (a *App) syncOpenVPNUDPPorts(ctx context.Context, serverID int64, prevPorts, nextPorts []int) {
	a.wgMu.Lock()
	defer a.wgMu.Unlock()
	raw := a.fetchRawConfig(ctx)
	doc, err := parseSEConfig(raw)
	if err != nil {
		fail(502, "SoftEther returned an invalid server configuration.")
	}
	sc := doc.Root.child("ServerConfiguration")
	if sc == nil || sc.item("PortsUDP") == nil {
		fail(502, "The server configuration does not expose UDP listener ports.")
	}
	existing := parseUDPPorts(sc.item("PortsUDP").Val)
	wgPort := a.savedWireguardPort(serverID)
	reserved := map[int]bool{443: true, 992: true, 1194: true, 5555: true}
	dropSet := map[int]bool{}
	nextSet := map[int]bool{}
	for _, p := range nextPorts {
		nextSet[p] = true
	}
	for _, p := range prevPorts {
		if !nextSet[p] && !reserved[p] && p != wgPort {
			dropSet[p] = true
		}
	}
	seen := map[int]bool{}
	merged := make([]int, 0, len(existing)+len(nextPorts))
	for _, p := range existing {
		if p >= 1 && p <= 65535 && !dropSet[p] && !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	for _, p := range nextPorts {
		if p >= 1 && p <= 65535 && !seen[p] {
			seen[p] = true
			merged = append(merged, p)
		}
	}
	sort.Ints(merged)
	edit := setItemEdit(doc, sc, "string", "PortsUDP", formatUDPPorts(merged))
	updated, err := applySEEdits(raw, []seEdit{edit})
	check(err)
	if updated == raw {
		return
	}
	a.pushWireGuardConfig(ctx, updated)
}
