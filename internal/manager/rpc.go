package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

//go:embed rpc-spec.json
var rpcSpec []byte

//go:embed routes.json
var routeSpec []byte

type RPCClient struct {
	http                *http.Client
	url, password, auth string
	id                  atomic.Uint64
}

func NewRPCClient(host string, port int, password, auth string) *RPCClient {
	// SoftEther installations normally use their own self-signed server certificate.
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second}
	return &RPCClient{http: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, url: "https://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/api/", password: password, auth: auth}
}
func (c *RPCClient) Close() { c.http.CloseIdleConnections() }
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
	if c.auth != "basic" {
		req.Header.Set("X-VPNADMIN-HUBNAME", "")
		req.Header.Set("X-VPNADMIN-PASSWORD", c.password)
	}
	if c.auth == "basic" || c.auth == "both" {
		req.SetBasicAuth("administrator", c.password)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, httpError{502, M{"message": "Cannot connect to SoftEther: " + err.Error(), "se_error": "TransportError", "se_code": nil}}
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
		return nil, httpError{502, M{"message": "Invalid SoftEther JSON response.", "se_error": "ProtocolError"}}
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
			{"OBJECT_NOT_FOUND|NOT_FOUND|NO_SUCH|NOT_EXISTS|OBJECT_EXISTS_NOT", 404, "NotFoundError"}, {"ALREADY_EXISTS|OBJECT_EXISTS|LOCAL_BRIDGE_EXISTS|DUPLICATE", 409, "AlreadyExistsError"},
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
		return c, nil
	}
	pwd, err := a.decrypt(str(row["EncryptedPassword"]))
	if err != nil {
		return nil, err
	}
	c := NewRPCClient(str(row["Host"]), int(integer(row["Port"])), pwd, str(row["AuthType"]))
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
	}
	client, err := a.clientFor(a.serverIDFromContext(c))
	check(err)
	return client
}
func (a *App) rpc(ctx context.Context, method string, params M) M {
	out, err := a.client(ctx).Call(ctx, method, params)
	check(err)
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
	execute(a.db, `UPDATE Server SET Host=?,Port=?,EncryptedPassword=?,AuthType=?,UpdatedDate=? WHERE ServerID=?`, row["Host"], row["Port"], row["EncryptedPassword"], row["AuthType"], now(), id)
	a.invalidateClientLocked(id)
	if boolean(row["IsDefault"]) {
		a.syncDefaultConnection(row)
	}
}
func (a *App) probeServer(ctx context.Context) (result M) {
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
	return M{"online": true, "configured": true, "version": info["ServerVersionString_str"], "server_type": info["ServerType_u32"], "hostname": info["ServerHostName_str"], "sessions": status["NumSessionsTotal_u32"], "users": status["NumUsers_u32"], "hubs": status["NumHubTotal_u32"], "send_bytes": status["SendBytes_u64"], "recv_bytes": status["RecvBytes_u64"], "started": status["StartTime_dt"], "current_time": status["CurrentTime_dt"]}
}
func (a *App) testConnection(ctx context.Context, host string, port int, password, auth string) M {
	modes := []string{"header", "basic", "both"}
	if auth != "" {
		modes = []string{auth}
	}
	var last error
	for _, mode := range modes {
		c := NewRPCClient(host, port, password, mode)
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
			out := a.rpc(r.Context(), route.RPC, params)
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
func documentedMethods() []M {
	var out []M
	check(json.Unmarshal(rpcSpec, &out))
	for _, name := range []string{"GetVgsConfig", "SetVgsConfig"} {
		out = append(out, M{"name": name, "title": name, "desc": "VPN Gate builds only.", "params": []any{}, "input": M{}})
	}
	return out
}
