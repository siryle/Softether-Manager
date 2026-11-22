package manager

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

type httpError struct {
	Status int
	Detail any
}

func (e httpError) Error() string { return fmt.Sprint(e.Detail) }
func fail(status int, detail any) { panic(httpError{status, detail}) }
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func integer(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case bool:
		if n {
			return 1
		}
		return 0
	}
	n, _ := strconv.ParseInt(str(v), 10, 64)
	return n
}
func number(v any) float64 { n, _ := strconv.ParseFloat(str(v), 64); return n }
func boolean(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case nil:
		return false
	case string:
		return n != "" && n != "false" && n != "0"
	default:
		return integer(v) != 0
	}
}
func object(v any) M {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return M{}
}
func list(v any) []M {
	out := []M{}
	switch values := v.(type) {
	case []any:
		for _, x := range values {
			out = append(out, object(x))
		}
	case []M:
		return values
	}
	return out
}
func clone(m M) M {
	out := M{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
func withDefault(m M, key string, fallback any) any {
	if v, ok := m[key]; ok {
		return v
	}
	return fallback
}
func readBody(w http.ResponseWriter, r *http.Request) M {
	var body M
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	d.UseNumber()
	if err := d.Decode(&body); err != nil {
		fail(422, "A JSON object body is required.")
	}
	if body == nil {
		fail(422, "A JSON object body is required.")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		fail(422, "Only one JSON object is allowed.")
	}
	return body
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type contextKey string

const (
	serverIDKey  contextKey = "server_id"
	rpcClientKey contextKey = "rpc_client"
)

type inflightCall struct {
	wg       sync.WaitGroup
	val      any
	panicVal any
}

type App struct {
	cfg                                            Config
	db                                             *sql.DB
	rdb                                            *sql.DB
	sessionKey, encryptionKey                      string
	mux                                            *http.ServeMux
	setupMu, connectionMu, quotaMu, accessMu, wgMu sync.Mutex
	quotaLocks                                     map[int64]*sync.Mutex
	rpcClients                                     map[int64]*RPCClient
	logins                                         loginLimiter
	resources                                      resourceState
	tls                                            *tlsManager
	updates                                        updateState
	cancel                                         context.CancelFunc
	workers                                        sync.WaitGroup
	geoMu                                          sync.RWMutex
	xdb                                            *xdb.Searcher
	xdbPath                                        string
	geoNegMu                                       sync.Mutex
	geoNegCache                                    map[string]time.Time
	loginObsMu                                     sync.Mutex
	loginObs                                       map[string]userLoginObservation
	userMetaMu                                     sync.RWMutex
	userMeta                                       map[string]hubUserMetaCache
	sfMu                                           sync.Mutex
	sfFlights                                      map[string]*inflightCall
	pruneMu                                        sync.Mutex
	lastPruneAt                                    time.Time
	wgTrackMu                                      sync.Mutex
	wgTrack                                        map[string]*wgSessTrack
	configCacheMu                                  sync.RWMutex
	configCache                                    map[int64]rawConfigCacheEntry
	liveTrafficMu                                  sync.Mutex
	liveTraffic                                    map[string][]liveTrafficPoint
}

type rawConfigCacheEntry struct {
	raw       string
	fetchedAt time.Time
}

// singleflight coalesces concurrent identical read RPC flows without caching stale data.
func (a *App) singleflight(key string, fn func() any) any {
	a.sfMu.Lock()
	if a.sfFlights == nil {
		a.sfFlights = make(map[string]*inflightCall)
	}
	if call, ok := a.sfFlights[key]; ok {
		a.sfMu.Unlock()
		call.wg.Wait()
		if call.panicVal != nil {
			panic(call.panicVal)
		}
		return call.val
	}
	call := &inflightCall{}
	call.wg.Add(1)
	a.sfFlights[key] = call
	a.sfMu.Unlock()

	defer func() {
		if p := recover(); p != nil {
			call.panicVal = p
		}
		a.sfMu.Lock()
		delete(a.sfFlights, key)
		a.sfMu.Unlock()
		call.wg.Done()
		if call.panicVal != nil {
			panic(call.panicVal)
		}
	}()
	call.val = fn()
	return call.val
}

func (a *App) readDB() *sql.DB {
	if a.rdb != nil {
		return a.rdb
	}
	return a.db
}

func New(cfg Config) (app *App, err error) {
	defer func() {
		if v := recover(); v != nil {
			if app != nil {
				if app.rdb != nil && app.rdb != app.db {
					app.rdb.Close()
				}
				if app.db != nil {
					app.db.Close()
				}
			}
			app = nil
			err = fmt.Errorf("initialize: %v", v)
		}
	}()
	if err = os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	db, err := openDB(cfg.Database)
	if err != nil {
		return nil, err
	}
	rdb, err := openReadDB(cfg.Database, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	app = &App{cfg: cfg, db: db, rdb: rdb, mux: http.NewServeMux(), rpcClients: make(map[int64]*RPCClient)}
	if err = app.initSecrets(); err != nil {
		if rdb != db {
			rdb.Close()
		}
		db.Close()
		return nil, err
	}
	app.initGeoIP()
	if err = app.protectExistingBackups(); err != nil {
		if rdb != db {
			rdb.Close()
		}
		db.Close()
		return nil, fmt.Errorf("restrict backup permissions: %w", err)
	}
	app.migrateSecretSettings()
	app.migrateCredentials()
	// An intentionally empty management password is still a configured connection.
	if boolean(app.setting("se_configured")) {
		execute(db, `UPDATE Server SET EncryptedPassword=? WHERE IsDefault=1 AND EncryptedPassword=''`, app.encrypt(""))
	}
	app.tls = newTLSManager(app)
	app.register()
	return app, nil
}
func (a *App) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	a.workers.Wait()
	a.tls.close()
	a.geoMu.Lock()
	if a.xdb != nil {
		a.xdb.Close()
		a.xdb = nil
	}
	a.geoMu.Unlock()
	a.connectionMu.Lock()
	for _, c := range a.rpcClients {
		if c != nil {
			c.Close()
		}
	}
	a.rpcClients = make(map[int64]*RPCClient)
	a.connectionMu.Unlock()
	if a.rdb != nil && a.rdb != a.db {
		_ = a.rdb.Close()
	}
	return a.db.Close()
}
func (a *App) bootstrap() {
	if a.cfg.AdminUsername != "" && a.cfg.AdminPassword != "" && integer(one(a.db, `SELECT COUNT(*) n FROM PanelUser WHERE IsDeleted=0`)["n"]) == 0 {
		a.SetPassword(a.cfg.AdminUsername, a.cfg.AdminPassword)
	}
	if a.cfg.SEPassword != "" && integer(one(a.db, `SELECT COUNT(*) n FROM Server`)["n"]) == 0 {
		stamp := now()
		enc := a.encrypt(a.cfg.SEPassword)
		execute(a.db, `INSERT INTO Server(Name,Host,Port,EncryptedPassword,AuthType,IsEnabled,IsDefault,SortOrder,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,1,1,0,?,?)`, "Default Server", a.cfg.SEHost, a.cfg.SEPort, enc, a.cfg.SEAuth, stamp, stamp)
	}
	if a.cfg.SEPassword != "" && !boolean(a.connection()["configured"]) {
		a.saveConnection(a.cfg.SEHost, a.cfg.SEPort, &a.cfg.SEPassword, a.cfg.SEAuth)
	}
}
func (a *App) Start(ctx context.Context) {
	ctx, a.cancel = context.WithCancel(ctx)
	a.bootstrap()
	a.startWorkers(ctx)
	a.tls.start(ctx)
}

const localHealthPath = "/_sem/health"

func (a *App) Handler() http.Handler {
	prefix := strings.Trim(str(a.setting("web_path")), "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if e, ok := p.(httpError); ok {
					writeJSON(w, e.Status, M{"detail": e.Detail})
				} else {
					log.Printf("request %s %s: %v", r.Method, r.URL.Path, p)
					writeJSON(w, 500, M{"detail": "Internal server error."})
				}
			}
		}()
		if r.URL.Path == localHealthPath && (r.Method == "GET" || r.Method == "HEAD") && localProbe(r) && hostName(r.Host) == "localhost" {
			writeJSON(w, 200, M{"ok": true, "version": a.cfg.Version})
			return
		}
		if !a.hostAllowed(r) {
			writeJSON(w, 404, M{"detail": "Not found."})
			return
		}
		if a.tls.challenge(w, r) {
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			for _, allowed := range a.cfg.CORS {
				if origin == strings.TrimSpace(allowed) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Credentials", "true")
					w.Header().Add("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Server-ID")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					break
				}
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && !a.sameOrigin(r) {
			fail(403, "Request origin is not allowed.")
		}
		if prefix != "" {
			mount := "/" + prefix
			if r.URL.Path == mount {
				http.Redirect(w, r, mount+"/", http.StatusTemporaryRedirect)
				return
			}
			if !strings.HasPrefix(r.URL.Path, mount+"/") {
				writeJSON(w, 404, M{"detail": "Not found."})
				return
			}
			r = r.Clone(r.Context())
			u := *r.URL
			r.URL = &u
			r.URL.Path = strings.TrimPrefix(r.URL.Path, mount)
			if r.URL.RawPath != "" {
				r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, mount)
			}
		}
		a.mux.ServeHTTP(w, r)
	})
}
func (a *App) serverIDFromContext(ctx context.Context) int64 {
	if ctx != nil {
		if id, ok := ctx.Value(serverIDKey).(int64); ok && id > 0 {
			return id
		}
	}
	row := one(a.db, `SELECT ServerID FROM Server WHERE IsDefault=1 LIMIT 1`)
	if row == nil {
		row = one(a.db, `SELECT ServerID FROM Server ORDER BY ServerID LIMIT 1`)
	}
	if row != nil {
		return integer(row["ServerID"])
	}
	return 0
}

func (a *App) handle(pattern string, public bool, fn func(http.ResponseWriter, *http.Request, M) any) {
	a.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		var user M
		if !public {
			user = a.requireUser(r)
		}
		sid := r.Header.Get("X-Server-ID")
		if sid == "" {
			sid = r.URL.Query().Get("server_id")
		}
		if !public && sid != "" {
			id, err := strconv.ParseInt(sid, 10, 64)
			if err != nil || id <= 0 {
				fail(422, "Invalid server id.")
			}
			// Panel and connection-list endpoints must remain usable after a saved server is removed.
			path := strings.TrimPrefix(r.URL.Path, "/api/v1")
			scoped := strings.HasPrefix(path, "/server/") || strings.HasPrefix(path, "/hubs") || path == "/users" || path == "/clients" || strings.HasPrefix(path, "/quotas")
			if scoped {
				a.serverRow(id)
			}
			r = r.WithContext(context.WithValue(r.Context(), serverIDKey, id))
		}
		if !public {
			id := a.serverIDFromContext(r.Context())
			path := strings.TrimPrefix(r.URL.Path, "/api/v1")
			if r.Method != "GET" && r.Method != "HEAD" {
				var writeID int64
				switch {
				case r.PathValue("id") != "" && strings.HasPrefix(path, "/servers/") && (r.Method == "PUT" || r.Method == "DELETE"):
					writeID = serverID(r)
				case strings.HasPrefix(path, "/hubs/") || path == "/hubs" || (strings.HasPrefix(path, "/server/") && path != "/server/connection/test" && path != "/server/probe"):
					writeID = id
				}
				if writeID != 0 {
					lock := a.quotaLock(writeID)
					lock.Lock()
					defer lock.Unlock()
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), serverIDKey, id))
			user["ServerID"] = id
		}
		result := fn(w, r, user)
		if result != nil {
			writeJSON(w, 200, result)
		}
	})
}
func (a *App) requireUser(r *http.Request) M {
	token := requestToken(r)
	claims := a.claims(token)
	if claims == nil {
		fail(401, "Not signed in.")
	}
	user := one(a.db, `SELECT UserID,Username,SessionVersion FROM PanelUser WHERE UserID=? AND IsDeleted=0`, integer(claims["uid"]))
	if user == nil || integer(claims["ver"]) != integer(user["SessionVersion"]) || one(a.db, `SELECT 1 FROM RevokedSession WHERE TokenHash=?`, tokenHash(token)) != nil {
		fail(401, "This session is no longer valid. Sign in again.")
	}
	return user
}
func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.TrimRight(strings.Trim(host, "[]"), "."))
}
func loopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func localProbe(r *http.Request) bool {
	if loopback(r) {
		return true
	}
	// An explicitly bound local address is probed from that same interface.
	local, _ := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && local != nil && local.IP.Equal(net.ParseIP(remote))
}

func (a *App) hostAllowed(r *http.Request) bool {
	domain := str(a.setting("domain"))
	localHost := hostName(r.Host)
	local := localHost == "localhost" || localHost == "127.0.0.1" || localHost == "::1"
	return !boolean(a.setting("domain_only")) || domain == "" || (loopback(r) && local) || strings.HasPrefix(r.URL.Path, "/.well-known/acme-challenge/") || localHost == domain
}
func (a *App) static(w http.ResponseWriter, r *http.Request, _ M) any {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(404, "Not found.")
	}
	root := filepath.Join(a.cfg.Root, "app", "web", "out")
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		if r.URL.Path != "/" {
			fail(404, "Not found.")
		}
		return M{"detail": "Build the frontend with npm run build in app/web.", "version": a.cfg.Version}
	}
	http.FileServer(http.Dir(root)).ServeHTTP(w, r)
	return nil
}
