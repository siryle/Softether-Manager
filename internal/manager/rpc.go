package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed routes.json
var routeSpec []byte

type RPCClient struct {
	http                                   *http.Client
	url, password, auth, encryptedPassword string
	id                                     atomic.Uint64
	host                                   string
	port                                   int
}

func NewRPCClient(host string, port int, password, auth string) *RPCClient {
	// Standalone clients use normal PKI validation; panel clients add persisted pinning.
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxIdleConns: 64, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	return &RPCClient{http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, url: "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/api/", password: password, auth: auth, host: host, port: port}
}
func (c *RPCClient) Close() { c.http.CloseIdleConnections() }
func isTransportRestartCandidate(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

func isRestartingConfigError(err error) bool {
	he, ok := err.(httpError)
	return ok && boolean(object(he.Detail)["restarting_candidate"])
}

func (c *RPCClient) Call(ctx context.Context, method string, params M) (M, error) {
	if params == nil {
		params = M{}
	}
	// SoftEther reads and returns JSON-RPC IDs as strings (JsonGetStr/JsonSetStr).
	id := strconv.FormatUint(c.id.Add(1), 10)
	payload, err := json.Marshal(M{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SoftEther-Manager-Go")
	// SoftEther's AddHttpValueStr rejects any header with an empty value and closes
	// the socket (EOF); omit X-VPNADMIN-HUBNAME for server-admin mode and use
	// Basic auth when the password is empty.
	if c.auth != "basic" && c.password != "" {
		req.Header.Set("X-VPNADMIN-PASSWORD", c.password)
	}
	if c.auth == "basic" || c.auth == "both" || c.password == "" {
		req.SetBasicAuth("administrator", c.password)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, httpError{502, M{"message": "Cannot connect to SoftEther: " + err.Error(), "se_error": "TransportError", "se_code": nil, "restarting_candidate": isTransportRestartCandidate(err)}}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		kind, msg := "TransportError", fmt.Sprintf("SoftEther returned HTTP %d", res.StatusCode)
		if res.StatusCode == 401 || res.StatusCode == 403 {
			kind = "AuthenticationError"
			msg = "SoftEther rejected the administrator password."
		}
		if res.StatusCode == 404 {
			kind = "ApiDisabledError"
			msg = "SoftEther JSON-RPC Web API is disabled or unavailable."
		}
		return nil, httpError{502, M{"message": msg, "se_error": kind, "se_code": nil}}
	}
	var reply struct {
		ID     string `json:"id"`
		Result M      `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 32<<20))
	decoder.UseNumber()
	if err = decoder.Decode(&reply); err != nil {
		return nil, httpError{502, M{"message": "Invalid SoftEther JSON response.", "se_error": "ProtocolError", "restarting_candidate": isTransportRestartCandidate(err)}}
	}
	if reply.ID != id {
		return nil, httpError{502, M{"message": "SoftEther response ID mismatch.", "se_error": "ProtocolError"}}
	}
	if reply.Error != nil {
		status, kind := 400, "RpcError"
		message := strings.ToUpper(reply.Error.Message)
		for _, rule := range []struct {
			needles string
			status  int
			kind    string
		}{
			{"NOT_SUPPORTED|UNSUPPORTED|METHOD_NOT_FOUND|METHOD NOT FOUND", 400, "UnsupportedError"},
			{"OBJECT_NOT_FOUND|NOT_FOUND|NO_SUCH|NOT_EXISTS|OBJECT_EXISTS_NOT|OBJECT NOT FOUND|USER NOT FOUND|HUB NOT FOUND", 404, "NotFoundError"}, {"ALREADY_EXISTS|OBJECT_EXISTS|LOCAL_BRIDGE_EXISTS|DUPLICATE", 409, "AlreadyExistsError"},
			{"AUTH_FAILED|ACCESS_DENIED|NOT_ADMINPACK|ADMIN_PASSWORD|PASSWORD_IS_WRONG", 502, "AuthenticationError"}, {"NOT_ENOUGH_RIGHT|SECURITY_POLICY|ADMIN_ONLY", 403, "PermissionError"}, {"INVALID_", 400, "InvalidParameterError"},
		} {
			matched := false
			for _, needle := range strings.Split(rule.needles, "|") {
				if strings.Contains(message, needle) {
					status, kind = rule.status, rule.kind
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		return nil, httpError{status, M{"message": reply.Error.Message, "se_error": kind, "se_code": reply.Error.Code}}
	}
	if reply.Result == nil {
		return nil, httpError{502, M{"message": "Missing SoftEther result.", "se_error": "ProtocolError"}}
	}
	return reply.Result, nil
}
func (a *App) connection(ctx ...context.Context) M {
	var c context.Context
	if len(ctx) > 0 {
		c = ctx[0]
	}
	row := a.serverRow(a.serverIDFromContext(c))
	out := publicServer(row)
	out["server_id"] = row["ServerID"]
	if str(out["public_host"]) == "" && boolean(row["IsDefault"]) {
		out["public_host"] = a.setting("vpn_public_host")
	}
	if integer(row["PublicPort"]) <= 0 && boolean(row["IsDefault"]) {
		if legacyPort := integer(a.setting("vpn_public_port")); legacyPort > 0 {
			out["public_port"] = legacyPort
		}
	}
	return out
}

func (a *App) clientFor(serverID int64) (*RPCClient, error) {
	a.connectionMu.Lock()
	defer a.connectionMu.Unlock()
	row := one(a.db, `SELECT * FROM Server WHERE ServerID=?`, serverID)
	if row == nil {
		return nil, httpError{404, "Server not found."}
	}
	if !boolean(row["IsEnabled"]) {
		return nil, httpError{409, "This server is disabled."}
	}
	if str(row["EncryptedPassword"]) == "" {
		return nil, httpError{409, "The server connection is not configured."}
	}
	if c := a.rpcClients[serverID]; c != nil {
		if c.url == "https://"+net.JoinHostPort(str(row["Host"]), str(row["Port"]))+"/api/" && c.auth == str(row["AuthType"]) && c.encryptedPassword == str(row["EncryptedPassword"]) {
			return c, nil
		}
		a.invalidateClientLocked(serverID)
	}
	pwd, err := a.decrypt(str(row["EncryptedPassword"]))
	if err != nil {
		return nil, err
	}
	c := a.newRPCClient(str(row["Host"]), int(integer(row["Port"])), pwd, str(row["AuthType"]))
	c.encryptedPassword = str(row["EncryptedPassword"])
	a.rpcClients[serverID] = c
	return c, nil
}
func (a *App) invalidateClientLocked(serverID int64) {
	if c := a.rpcClients[serverID]; c != nil {
		c.Close()
		delete(a.rpcClients, serverID)
	}
}
func (a *App) client(ctx ...context.Context) *RPCClient {
	var c context.Context
	if len(ctx) > 0 {
		c = ctx[0]
		if c != nil {
			if client, ok := c.Value(rpcClientKey).(*RPCClient); ok {
				return client
			}
		}
	}
	client, err := a.clientFor(a.serverIDFromContext(c))
	check(err)
	return client
}
func (a *App) rpc(ctx context.Context, method string, params M) M {
	switch method {
	case "SetServerCert":
		chain, certErr := base64.StdEncoding.DecodeString(str(params["Cert_bin"]))
		key, keyErr := base64.StdEncoding.DecodeString(str(params["Key_bin"]))
		if certErr != nil || keyErr != nil {
			fail(422, "The certificate and key must be valid base64.")
		}
		out, err := a.installServerCertificate(ctx, chain, key)
		check(err)
		return out
	case "RegenerateServerCert":
		return a.regenerateServerCertificate(ctx, str(params["StrValue_str"]))
	case "SetConfig":
		id := a.serverIDFromContext(ctx)
		a.requireReleasedQuotas(id)
		data, err := base64.StdEncoding.DecodeString(str(params["FileData_bin"]))
		if err != nil || len(data) == 0 {
			fail(422, "A non-empty base64 configuration is required.")
		}
		fingerprint := configCertificateFingerprint(string(data))
		client := a.client(ctx)
		out, err := client.Call(ctx, method, params)
		if err != nil && !isRestartingConfigError(err) {
			check(err)
		}
		func() {
			a.connectionMu.Lock()
			defer a.connectionMu.Unlock()
			if fingerprint != "" {
				// Trust only the certificate supplied in the authorized restore, even across a restart.
				execute(a.db, `UPDATE RPCTrust SET PendingFingerprint=CASE WHEN Fingerprint=? THEN '' ELSE ? END WHERE Host=? AND Port=?`, fingerprint, fingerprint, client.host, client.port)
			}
			a.invalidateClientLocked(id)
		}()
		a.invalidateUserMetaCache(id, "")
		a.invalidateConfigCache(id)
		if out == nil {
			out = M{}
		}
		out["restarting"] = true
		return out
	}
	if (method == "SetUser" || method == "CreateUser") && params["Auth_Password_str"] != nil {
		params = clone(params)
		delete(params, "HashedKey_bin")
		delete(params, "NtLmSecureHash_bin")
	}
	if method == "SetHubRadius" && params["RadiusSecret_str"] == nil {
		params = clone(params)
		current := a.rpc(ctx, "GetHubRadius", M{"HubName_str": params["HubName_str"]})
		params["RadiusSecret_str"] = str(current["RadiusSecret_str"])
	}
	out, err := a.client(ctx).Call(ctx, method, params)
	check(err)
	if !strings.HasPrefix(method, "Get") && !strings.HasPrefix(method, "Enum") && !strings.HasPrefix(method, "Test") && !strings.HasPrefix(method, "Read") {
		a.invalidateConfigCache(a.serverIDFromContext(ctx))
	}
	switch method {
	case "SetUser", "CreateUser", "DeleteUser", "DeleteHub":
		a.invalidateUserMetaCache(a.serverIDFromContext(ctx), str(params["HubName_str"]))
	}
	return out
}
func (a *App) syncDefaultConnection(row M) {
	a.set("se_host", row["Host"])
	a.set("se_port", integer(row["Port"]))
	a.set("se_password", row["EncryptedPassword"])
	a.set("se_auth", row["AuthType"])
	a.set("se_configured", str(row["EncryptedPassword"]) != "")
}
func (a *App) saveConnection(host string, port int, password *string, auth string, ctx ...context.Context) {
	var c context.Context
	if len(ctx) > 0 {
		c = ctx[0]
	}
	a.connectionMu.Lock()
	defer a.connectionMu.Unlock()
	id := a.serverIDFromContext(c)
	row := a.serverRow(id)
	retargeting := !strings.EqualFold(str(row["Host"]), host) || integer(row["Port"]) != int64(port)
	if retargeting {
		a.requireReleasedQuotas(id)
		a.requireUnassignedCertificateTarget(id)
	}
	if one(a.db, `SELECT ServerID FROM Server WHERE lower(Host)=lower(?) AND Port=? AND ServerID!=?`, host, port, id) != nil {
		fail(409, "A server with this address and port already exists.")
	}
	row["Host"], row["Port"] = host, port
	if password != nil {
		row["EncryptedPassword"] = a.encrypt(*password)
	}
	if auth != "" {
		row["AuthType"] = auth
	}
	update := `UPDATE Server SET Host=?,Port=?,EncryptedPassword=?,AuthType=?,UpdatedDate=? WHERE ServerID=?`
	if retargeting {
		update += ` AND NOT EXISTS (SELECT 1 FROM TrafficQuota WHERE ServerID=Server.ServerID AND RestoreState!='')`
	}
	result, err := a.db.Exec(update, row["Host"], row["Port"], row["EncryptedPassword"], row["AuthType"], now(), id)
	check(err)
	changed, err := result.RowsAffected()
	check(err)
	if changed != 1 {
		fail(409, "The server or its quotas changed during the connection update. Try again.")
	}
	if retargeting {
		a.resetQuotaBaselines(id)
		execute(a.db, `UPDATE Server SET BackupLastRun='',BackupLastError='' WHERE ServerID=?`, id)
	}
	a.invalidateClientLocked(id)
	a.invalidateUserMetaCache(id, "")
	a.invalidateConfigCache(id)
	if boolean(row["IsDefault"]) {
		a.syncDefaultConnection(row)
	}
}

func (a *App) countHubClientSessions(ctx context.Context, hub string) (count int64, ok bool) {
	if hub == "" {
		return 0, false
	}
	defer func() {
		if recover() != nil {
			count, ok = 0, false
		}
	}()
	serverID := a.serverIDFromContext(ctx)
	wgTimeout := a.wireguardIdleTimeout()
	sessions := list(a.rpc(ctx, "EnumSession", M{"HubName_str": hub})["SessionList"])
	for _, s := range sessions {
		if !isClientSessionRPC(s) {
			continue
		}
		if wgTimeout > 0 && isWireGuardSession(s, nil) && a.isWireGuardReaped(serverID, hub, s) {
			continue
		}
		count++
	}
	return count, true
}

func (a *App) sumHubUserTraffic(ctx context.Context, hub string) (send, recv int64, ok bool) {
	if hub == "" {
		return 0, 0, false
	}
	defer func() {
		if recover() != nil {
			send, recv, ok = 0, 0, false
		}
	}()
	out := a.rpc(ctx, "EnumUser", M{"HubName_str": hub})
	rawList, hasList := out["UserList"]
	if !hasList {
		return 0, 0, false
	}
	users := list(rawList)
	a.updateUserMetaCache(a.serverIDFromContext(ctx), hub, users)
	validUsers := 0
	for _, u := range users {
		s, r, valid := userTrafficCounters(u)
		if !valid {
			continue
		}
		validUsers++
		send += s
		recv += r
	}
	if len(users) > 0 && validUsers == 0 {
		return 0, 0, false
	}
	return send, recv, true
}

func (a *App) enrichHubListStats(ctx context.Context, hubs []M, includeSessions, includeTraffic bool) (clientTotal int64, sessOK bool, totalSend, totalRecv int64, trafficOK bool) {
	if len(hubs) == 0 {
		return 0, true, 0, 0, true
	}
	type hubJob struct {
		name      string
		needSess  bool
		zeroSess  bool
		needUsers bool
	}
	type hubRes struct {
		sess       int64
		sessOK     bool
		send, recv int64
		trafficOK  bool
	}
	jobs := make([]hubJob, len(hubs))
	results := make([]hubRes, len(hubs))
	for i, h := range hubs {
		name := str(h["HubName_str"])
		raw := integer(h["NumSessions_u32"])
		offline := h["Online_bool"] != nil && !boolean(h["Online_bool"])
		zeroSess := name == "" || offline || (h["NumSessions_u32"] != nil && raw == 0)
		jobs[i] = hubJob{
			name:      name,
			needSess:  includeSessions && !zeroSess,
			zeroSess:  includeSessions && zeroSess,
			needUsers: includeTraffic && name != "",
		}
		if jobs[i].zeroSess {
			results[i].sessOK = true
		}
		if includeTraffic && name == "" {
			results[i].trafficOK = true
		}
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i, job := range jobs {
		if !job.needSess && !job.needUsers {
			continue
		}
		wg.Add(1)
		go func(idx int, j hubJob) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var inner sync.WaitGroup
			if j.needSess {
				inner.Add(1)
				go func() {
					defer inner.Done()
					results[idx].sess, results[idx].sessOK = a.countHubClientSessions(ctx, j.name)
				}()
			}
			if j.needUsers {
				inner.Add(1)
				go func() {
					defer inner.Done()
					results[idx].send, results[idx].recv, results[idx].trafficOK = a.sumHubUserTraffic(ctx, j.name)
				}()
			}
			inner.Wait()
		}(i, job)
	}
	wg.Wait()
	sessOK, trafficOK = includeSessions, includeTraffic
	for i, r := range results {
		if includeSessions {
			if r.sessOK {
				hubs[i]["ClientSessions_u32"] = r.sess
				clientTotal += r.sess
			} else {
				sessOK = false
			}
		}
		if includeTraffic {
			if r.trafficOK {
				hubs[i]["ClientSendBytes_u64"] = r.send
				hubs[i]["ClientRecvBytes_u64"] = r.recv
				totalSend += r.send
				totalRecv += r.recv
			} else {
				trafficOK = false
			}
		}
	}
	return clientTotal, sessOK, totalSend, totalRecv, trafficOK
}

func (a *App) probeServer(ctx context.Context, countClients ...bool) (result M) {
	row := a.serverRow(a.serverIDFromContext(ctx))
	if str(row["EncryptedPassword"]) == "" {
		return M{"online": false, "configured": false, "error": "not connected yet"}
	}
	defer func() {
		if p := recover(); p != nil {
			result = M{"online": false, "configured": true, "error": fmtError(p)}
		}
	}()
	info := a.rpc(ctx, "GetServerInfo", nil)
	status := a.rpc(ctx, "GetServerStatus", nil)
	sendBytes, recvBytes := bytesFor(status, "")
	if sendBytes == 0 && status["SendBytes_u64"] != nil {
		sendBytes = integer(status["SendBytes_u64"])
	}
	if recvBytes == 0 && status["RecvBytes_u64"] != nil {
		recvBytes = integer(status["RecvBytes_u64"])
	}
	sessions := status["NumSessionsTotal_u32"]
	if len(countClients) > 0 && countClients[0] {
		var hubs []M
		func() {
			defer func() { _ = recover() }()
			hubs = list(a.rpc(ctx, "EnumHub", nil)["HubList"])
		}()
		if len(hubs) > 0 {
			// Sum per-user counters to exclude Local Bridge / SecureNAT double-counting and LAN noise.
			clientTotal, sessOK, uSend, uRecv, trafficOK := a.enrichHubListStats(ctx, hubs, integer(sessions) > 0, true)
			if sessOK {
				sessions = clientTotal
			}
			if trafficOK {
				sendBytes, recvBytes = uSend, uRecv
			}
		}
	}
	return M{"online": true, "configured": true, "version": info["ServerVersionString_str"], "server_type": info["ServerType_u32"], "hostname": info["ServerHostName_str"], "sessions": sessions, "users": status["NumUsers_u32"], "hubs": status["NumHubTotal_u32"], "send_bytes": sendBytes, "recv_bytes": recvBytes, "started": status["StartTime_dt"], "current_time": status["CurrentTime_dt"]}
}
func (a *App) testConnection(ctx context.Context, host string, port int, password, auth string) M {
	modes := []string{"header", "basic", "both"}
	if auth != "" {
		modes = []string{auth}
	}
	var last error
	for _, mode := range modes {
		c := a.newRPCClient(host, port, password, mode)
		info, err := c.Call(ctx, "GetServerInfo", M{})
		if err != nil {
			c.Close()
			last = err
			continue
		}
		status, err := c.Call(ctx, "GetServerStatus", M{})
		c.Close()
		if err != nil {
			last = err
			continue
		}
		return M{"ok": true, "auth": mode, "version": info["ServerVersionString_str"], "hostname": info["ServerHostName_str"], "sessions": status["NumSessionsTotal_u32"]}
	}
	check(last)
	fail(502, "Connection test failed.")
	return nil
}

type rpcRoute struct {
	Method, Path, RPC string
	Params            M
	Body              bool
	Readback, Audit   string
}

func (a *App) registerRPCRoutes() {
	var routes []rpcRoute
	check(json.Unmarshal(routeSpec, &routes))
	for _, route := range routes {
		a.handle(route.Method+" /api/v1"+route.Path, false, func(w http.ResponseWriter, r *http.Request, user M) any {
			body := M{}
			if route.Body || r.Method == "POST" || r.Method == "PUT" {
				if route.Body || len(route.Params) > 0 {
					body = readBody(w, r)
				}
			}
			if route.RPC == "DeleteAccess" {
				a.accessMu.Lock()
				defer a.accessMu.Unlock()
				body = readBody(w, r)
				a.checkAccessVersion(r.Context(), r.PathValue("hub"), "AccessList", str(body["expected_version"]))
			}
			params := M{}
			if route.Body {
				params = clone(body)
			}
			for k, v := range route.Params {
				s, ok := v.(string)
				if ok && strings.HasPrefix(s, "$") {
					val := r.PathValue(s[1:])
					if val == "" {
						fail(422, "Missing path parameter: "+s[1:])
					}
					if strings.HasSuffix(k, "_u32") {
						n, err := strconv.ParseUint(val, 10, 32)
						if err != nil {
							fail(422, "Invalid integer: "+val)
						}
						params[k] = n
					} else {
						params[k] = val
					}
				} else if ok && strings.HasPrefix(s, "@") {
					val, exists := body[s[1:]]
					if !exists {
						if s == "@enable" {
							val = true
						} else {
							fail(422, "Missing field: "+s[1:])
						}
					}
					params[k] = val
				} else {
					params[k] = v
				}
			}
			validateWire(params)
			a.guardHubOnline(r.Context(), route.RPC, params)
			out := a.rpc(r.Context(), route.RPC, params)
			if route.RPC == "DeleteSession" {
				a.markSessionClosedNow(a.serverIDFromContext(r.Context()), str(params["HubName_str"]), str(params["Name_str"]), "Disconnected by administrator")
			}
			if route.RPC == "EnumAccess" {
				out["version"] = accessVersion(out["AccessList"])
			} else if route.RPC == "GetAcList" {
				out["version"] = accessVersion(out["ACList"])
			} else if route.RPC == "EnumHub" {
				a.enrichHubListStats(r.Context(), list(out["HubList"]), true, false)
			} else if route.RPC == "GetHubStatus" {
				hubName := str(params["HubName_str"])
				var hubWG sync.WaitGroup
				var clientSess int64
				var sessOK bool
				var uSend, uRecv int64
				var trafficOK bool
				if out["NumSessions_u32"] != nil && integer(out["NumSessions_u32"]) == 0 {
					out["ClientSessions_u32"] = int64(0)
				} else {
					hubWG.Add(1)
					go func() {
						defer hubWG.Done()
						clientSess, sessOK = a.countHubClientSessions(r.Context(), hubName)
					}()
				}
				hubWG.Add(1)
				go func() {
					defer hubWG.Done()
					uSend, uRecv, trafficOK = a.sumHubUserTraffic(r.Context(), hubName)
				}()
				hubWG.Wait()
				if sessOK {
					out["ClientSessions_u32"] = clientSess
				}
				if trafficOK {
					out["ClientSendBytes_u64"] = uSend
					out["ClientRecvBytes_u64"] = uRecv
				}
			}
			if route.Audit != "" {
				a.audit(user, route.Audit, "server", r.PathValue("hub"), r.PathValue("name"))
			}
			if route.Readback != "" {
				out = a.rpc(r.Context(), route.Readback, route.Params)
			}
			return out
		})
	}
}
func validateWire(params M) {
	for key, val := range params {
		if strings.HasSuffix(key, "_u32") || strings.HasSuffix(key, "_u64") {
			// The wire suffix also applies to arrays, such as PacketLogConfig_u32.
			values, ok := val.([]any)
			if !ok {
				values = []any{val}
			}
			for _, value := range values {
				n, err := strconv.ParseUint(str(value), 10, 64)
				if err != nil || (strings.HasSuffix(key, "_u32") && n > 1<<32-1) {
					fail(422, "Invalid unsigned integer field: "+key)
				}
			}
		}
		if strings.HasSuffix(key, "_bool") {
			if _, ok := val.(bool); !ok {
				fail(422, "Expected boolean field: "+key)
			}
		}
	}
}
