package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProfileEditKeepsCurrentVPNPassword(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	path := "/api/v1/hubs/Office%20%2F%20%E4%B8%8A%E6%B5%B7/users/Alice"
	status, stale, _ := call(t, a, "GET", path, nil, token)
	if status != 200 {
		t.Fatal(status, stale)
	}
	status, out, _ := call(t, a, "PUT", path, M{"Auth_Password_str": "new password"}, token)
	if status != 200 {
		t.Fatal(status, out)
	}
	stale["Realname_utf"] = "Display name"
	status, out, _ = call(t, a, "PUT", path, stale, token)
	if status != 200 || fake.userHash != hashedPassword("new password", "Alice") {
		t.Fatal("saving an earlier record reverted the current password", status, out)
	}
}

func TestCSVExportUsesLiveCredentialAndRejectsLossyAuth(t *testing.T) {
	a, _ := bulkApp(t)
	token := admin(t, a)
	a.saveCredential(1, "VPN", "alice", hashedPassword("previous password", "alice"))
	status, out, _ := call(t, a, "GET", "/api/v1/hubs/VPN/users/export", nil, token)
	if status != 200 || !strings.Contains(str(out["csv"]), testPasswordHash) {
		t.Fatal("CSV did not export the live hash", status, out)
	}
	status, out, _ = call(t, a, "POST", "/api/v1/hubs/VPN/users/import", M{"csv": "name,auth_type\ncert-user,signed\n"}, token)
	if status != 200 || len(list(out["failed"])) != 1 {
		t.Fatal("CSV permitted incomplete authentication settings", status, out)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{{"Name_str": "cert-user", "AuthType_u32": 3}}}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	status, out, _ = call(t, a, "GET", "/api/v1/hubs/VPN/users/export", nil, token)
	if status != 409 {
		t.Fatal("CSV exported a user without its authentication settings", status, out)
	}
}

func connectAuditServer(t *testing.T, a *App, server *httptest.Server, id int64) {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	a.saveConnection(host, int(integer(port)), ptr(""), "header", context.WithValue(t.Context(), serverIDKey, id))
}

func TestGroupAssignmentFailureRollsBackImport(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var created, deniedFirst, removed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		result := M{}
		switch req["method"] {
		case "CreateUser":
			created.Store(true)
			p := object(req["params"])
			deniedFirst.Store(p["UsePolicy_bool"] == true && p["policy:Access_bool"] == false)
		case "SetUser":
			writeJSON(w, 200, M{"id": req["id"], "error": M{"code": 1, "message": "ERR_BUSY"}})
			return
		case "DeleteUser":
			removed.Store(true)
		}
		writeJSON(w, 200, M{"id": req["id"], "result": result})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	status, out, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", M{"csv": "name,password,group\nAlice,pw,restricted\n"}, token)
	if status != 200 || !created.Load() || !deniedFirst.Load() || !removed.Load() || len(list(out["failed"])) != 1 {
		t.Fatal("group failure did not remove a newly created account", status, out)
	}
}

func TestQuotaIgnoresMissingCountersAndProtectsRestore(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	fake := attachSoftEther(t, a)
	hub := "Office / 上海"
	a.saveQuota(1, "hub", hub, "", 1500, "total", true)
	a.quotaTick(t.Context())
	fake.mu.Lock()
	fake.missingTraffic = true
	fake.mu.Unlock()
	a.quotaTick(t.Context())
	fake.mu.Lock()
	fake.missingTraffic = false
	fake.mu.Unlock()
	a.quotaTick(t.Context())
	row := quotaPublic(a.quotaRow(1, "hub", hub, ""))
	if row["used_bytes"] != int64(1000) || row["blocked"] != false {
		t.Fatal("an incomplete reading changed the quota", row)
	}
	a.saveQuota(1, "hub", hub, "", 100, "total", true)
	a.quotaTick(t.Context())
	config := base64.StdEncoding.EncodeToString([]byte("test VPN config"))
	status, out, _ := call(t, a, "PUT", "/api/v1/server/config", M{"config_base64": config}, token)
	if status != 409 {
		t.Fatal("configuration restore did not protect the outstanding quota state", status, out)
	}
}

func TestBackupSchedulesAndPermissionsAreServerScoped(t *testing.T) {
	a := testApp(t)
	attachSoftEther(t, a)
	badServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer badServer.Close()
	id := execute(a.db, `INSERT INTO Server(Name,Host,Port) VALUES('Offline','offline.example',5555)`)
	connectAuditServer(t, a, badServer, id)
	a.backupTick(t.Context())
	good := a.serverRow(1)
	bad := a.serverRow(id)
	if str(good["BackupLastRun"]) == "" || str(bad["BackupLastRun"]) != "" || str(bad["BackupLastError"]) == "" {
		t.Fatal("backup result was not tracked per server")
	}
	files := a.listBackups(1)
	if len(files) != 1 {
		t.Fatal(files)
	}
	path := filepath.Join(a.getBackupDir(1), files[0].Filename)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup was not created with owner-only access", err)
	}
	check(os.Chmod(path, 0644))
	check(a.protectExistingBackups())
	info, err = os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("legacy backup permissions were not repaired", err)
	}
}

func TestHealthProbeDoesNotFollowPendingPath(t *testing.T) {
	a := testApp(t)
	a.set("web_path", "original")
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	a.set("web_path", "pending")
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	check(err)
	if probeHealth(host, int(integer(port)), a.cfg.Version, io.Discard) != 0 {
		t.Fatal("a pending path disabled the local probe")
	}
	if probeHealth(host, int(integer(port)), "wrong-version", io.Discard) == 0 {
		t.Fatal("probe accepted a different server version")
	}
}

type auditTransport func(*http.Request) (*http.Response, error)

func (f auditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type auditGeoBody struct {
	io.Reader
	closed *int
}

func (b auditGeoBody) Close() error { *b.closed++; return nil }

func TestWorkerRejectsQuotaReadFromOldEndpoint(t *testing.T) {
	a := testApp(t)
	fake := attachSoftEther(t, a)
	id, hub := int64(1), "Office / 上海"
	a.saveQuota(id, "hub", hub, "", 10, "total", true)
	old := a.client()
	cli, err := New(a.cfg)
	check(err)
	defer cli.Close()
	cli.saveConnection("other.example", 443, ptr("other password"), "header")
	ctx := context.WithValue(t.Context(), rpcClientKey, old)
	check(a.refreshQuota(ctx, id, "hub", hub, ""))
	row := a.quotaRow(id, "hub", hub, "")
	if integer(quotaPublic(row)["used_bytes"]) != 0 {
		t.Fatal("a reading from the previous server changed the new quota")
	}
	err = recoverAsError(func() error { a.blockQuota(ctx, row); return nil })
	if err == nil || a.quotaRow(id, "hub", hub, "")["RestoreState"] != "" {
		t.Fatal("an obsolete client reserved a quota block", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.hubOnline {
		t.Fatal("the obsolete endpoint was modified")
	}
}

func TestMissingQuotaSubjectCanBeRemoved(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var missing atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		if missing.Load() && req["method"] == "GetUser" {
			writeJSON(w, 200, M{"id": req["id"], "error": M{"code": 1, "message": "ERR_OBJECT_NOT_FOUND"}})
			return
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{"UsePolicy_bool": true, "policy:Access_bool": true, "Send.UnicastBytes_u64": 600, "Recv.UnicastBytes_u64": 400}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	a.saveQuota(1, "user", "VPN", "Alice", 100, "total", true)
	a.quotaTick(t.Context())
	if str(a.quotaRow(1, "user", "VPN", "Alice")["RestoreState"]) == "" {
		t.Fatal("test did not apply the quota")
	}
	missing.Store(true)
	status, out, _ := call(t, a, "DELETE", "/api/v1/quotas/user/VPN/Alice", nil, token)
	if status != 200 || a.quotaRow(1, "user", "VPN", "Alice") != nil {
		t.Fatal("quota restoration remained for a deleted user", status, out)
	}
}

func TestManualCertificateRotationContinuesToAuthenticate(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	var active atomic.Pointer[tls.Certificate]
	var replacements atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		if req["method"] == "SetServerCert" {
			p := object(req["params"])
			chain, err := base64.StdEncoding.DecodeString(str(p["Cert_bin"]))
			check(err)
			key, err := base64.StdEncoding.DecodeString(str(p["Key_bin"]))
			check(err)
			pair, err := tls.X509KeyPair(chain, key)
			check(err)
			active.Store(&pair)
			replacements.Add(1)
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{}})
	}))
	server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return &tls.Config{Certificates: []tls.Certificate{*active.Load()}}, nil
	}}
	server.StartTLS()
	defer server.Close()
	active.Store(&server.TLS.Certificates[0])
	connectAuditServer(t, a, server, 1)
	a.rpc(t.Context(), "GetServerInfo", nil)
	status, out, _ := call(t, a, "POST", "/api/v1/server/cert/regenerate", M{"common_name": "vpn.example.test"}, token)
	if status != 200 || replacements.Load() != 1 {
		t.Fatal("regeneration failed", status, out)
	}
	a.rpc(t.Context(), "GetServerInfo", nil)
}

func TestAutomaticCertificateDeploymentRetriesOnlyItsTarget(t *testing.T) {
	a := testApp(t)
	defaultServer := attachSoftEther(t, a)
	id := execute(a.db, `INSERT INTO Server(Name,Host,Port) VALUES('Target','target.example',5555)`)
	var pushes atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		if req["method"] == "SetServerCert" && pushes.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, id)
	pair := server.TLS.Certificates[0]
	name := server.Certificate().DNSNames[0]
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	key, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	check(err)
	check(saveCertificate(a.tls.certDir(name), chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), M{}))
	a.set("domain", name)
	a.set("cert_push_server_id", id)
	a.tls.deployCertificate(t.Context(), name)
	if pushes.Load() != 1 || str(a.tls.state["deployment_error"]) == "" {
		t.Fatal("certificate deployment failure was not recorded")
	}
	a.tls.state["deployment_attempt_at"] = time.Now().Add(-time.Minute).Format(time.RFC3339)
	a.tls.deployCertificate(t.Context(), name)
	a.tls.deployCertificate(t.Context(), name)
	if pushes.Load() != 2 || str(a.tls.state["deployment_error"]) != "" {
		t.Fatal("deployment was not retried exactly once")
	}
	defaultServer.mu.Lock()
	defer defaultServer.mu.Unlock()
	if len(defaultServer.calls) != 0 {
		t.Fatal("automatic deployment touched the default server")
	}
}

func TestHistoryQueriesUseTimeRangeIndexes(t *testing.T) {
	a := testApp(t)
	for _, tc := range []struct{ query, index string }{
		{`SELECT * FROM UserTrafficSample WHERE ServerID=1 AND HubName='VPN' AND UserName='Alice' COLLATE NOCASE AND julianday(SampledDate)>=julianday('2026-01-01') ORDER BY julianday(SampledDate),UserTrafficSampleID`, "IX_UserTrafficSample_History"},
		{`DELETE FROM VpnSessionTrafficSample WHERE datetime(SampledDate)<datetime('now','-30 days')`, "IX_VpnSessionTrafficSample_Retention"},
	} {
		plan := query(a.db, "EXPLAIN QUERY PLAN "+tc.query)
		if !strings.Contains(str(plan), tc.index) || strings.Contains(str(plan), "TEMP B-TREE") {
			t.Fatal("history query did not use its range index", plan)
		}
	}
}

func TestGeoFailuresCloseResponseAndRetry(t *testing.T) {
	a := testApp(t)
	original := http.DefaultTransport
	defer func() { http.DefaultTransport = original }()
	calls, closed := 0, 0
	http.DefaultTransport = auditTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Body: auditGeoBody{bytes.NewBufferString("rate limited"), &closed}, Header: http.Header{}}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	a.lookupIP(ctx, "198.51.100.127")
	first := calls
	a.lookupIP(ctx, "198.51.100.127")
	if first == 0 || calls != 2*first || calls != closed {
		t.Fatalf("failed responses or cache were left behind: calls=%d closed=%d", calls, closed)
	}
}
