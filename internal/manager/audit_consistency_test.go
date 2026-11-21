package manager

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientHistorySurvivesCleanupAndReopen(t *testing.T) {
	a := testApp(t)
	insert := func(name, key string) int64 {
		id := execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,StartedDate,LastSeenDate) VALUES(1,'VPN',?,?,?,?)`, name, key, now(), now())
		execute(a.db, `INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,SampledDate) VALUES(?,?)`, id, now())
		return id
	}
	for _, name := range []string{"linkedin", "LINKEDIN", "l3admin", "alice"} {
		insert(name, name)
	}
	for _, name := range []string{"link_office", "l3_router", "SecureNAT"} {
		insert(name, name)
	}
	if got := len(query(a.db, `SELECT * FROM VpnSessionSample WHERE `+clientSessionSQLClause)); got != 4 {
		t.Fatalf("normal users missing from reports: got %d", got)
	}
	a.prune()
	if got := len(query(a.db, `SELECT * FROM VpnSessionSample`)); got != 4 {
		t.Fatalf("cleanup deleted normal users or kept system sessions: %d", got)
	}
	insert("link_office", "legacy")
	reopened, err := New(a.cfg)
	check(err)
	defer reopened.Close()
	if got := len(query(reopened.db, `SELECT * FROM VpnSessionSample`)); got != 4 {
		t.Fatalf("startup migration deleted normal users or kept system sessions: %d", got)
	}
	if got := len(query(reopened.db, `SELECT * FROM VpnSessionTrafficSample`)); got != 4 {
		t.Fatalf("startup migration lost client traffic or orphaned system traffic: %d", got)
	}
}

func TestQuotaResetDiscardsInFlightReading(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	entered, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	var counter atomic.Int64
	counter.Store(200)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		send := counter.Load()
		if reads.Add(1) == 1 {
			send = 150
			close(entered)
			<-release
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{"Send.UnicastBytes_u64": send, "Recv.UnicastBytes_u64": 0}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	a.saveQuota(1, "hub", "VPN", "", 100000, "total", true)
	a.observeQuotaLocked(1, "hub", "VPN", "", 100, 0)
	done := make(chan struct{})
	go func() { a.quotaTick(t.Context()); close(done) }()
	<-entered
	status, out, _ := call(t, a, "POST", "/api/v1/quotas/hub/VPN/reset", nil, token)
	close(release)
	<-done
	if status != 200 || integer(out["used_bytes"]) != 0 {
		t.Fatal("reset failed", status, out)
	}
	if got := integer(quotaPublic(a.quotaRow(1, "hub", "VPN", ""))["used_bytes"]); got != 0 {
		t.Fatalf("stale reading undid reset: %d", got)
	}
	counter.Store(300)
	a.quotaTick(t.Context())
	if got := integer(quotaPublic(a.quotaRow(1, "hub", "VPN", ""))["used_bytes"]); got != 100 {
		t.Fatalf("fresh traffic after reset counted incorrectly: %d", got)
	}
}

func TestConfigurationChangesPreserveQuotaUsage(t *testing.T) {
	for _, path := range []string{"/api/v1/server/config", "/api/v1/server/ddns", "/api/v1/server/wireguard"} {
		t.Run(path, func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req M
				check(json.NewDecoder(r.Body).Decode(&req))
				writeJSON(w, 200, M{"id": req["id"], "result": M{"FileData_bin": base64.StdEncoding.EncodeToString([]byte(wgTestConfig5x))}})
			}))
			defer server.Close()
			connectAuditServer(t, a, server, 1)
			a.saveQuota(1, "hub", "DEFAULT", "", 100000, "total", true)
			a.observeQuotaLocked(1, "hub", "DEFAULT", "", 500, 100)
			body := M{"config_base64": base64.StdEncoding.EncodeToString([]byte(wgTestConfig5x)), "enabled": false, "wireguard_port": 51821, "confirm_restart": true}
			status, out, _ := call(t, a, "PUT", path, body, token)
			if status != 200 {
				t.Fatal(status, out)
			}
			if got := integer(quotaPublic(a.quotaRow(1, "hub", "DEFAULT", ""))["used_bytes"]); got != 600 {
				t.Fatalf("configuration change cleared quota usage: %d", got)
			}
		})
	}
}

func TestRejectedProtocolPortChangePreservesSavedPort(t *testing.T) {
	for _, protocol := range []string{"wireguard", "openvpn"} {
		for _, failure := range []string{"quota", "remote", "invalid-config"} {
			t.Run(protocol+"/"+failure, func(t *testing.T) {
				a := testApp(t)
				token := admin(t, a)
				var writes atomic.Int32
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req M
					check(json.NewDecoder(r.Body).Decode(&req))
					if req["method"] == "SetConfig" {
						writes.Add(1)
						w.WriteHeader(503)
						return
					}
					raw := wgTestConfig5x
					if failure == "invalid-config" {
						raw = "invalid configuration"
					}
					writeJSON(w, 200, M{"id": req["id"], "result": M{"FileData_bin": base64.StdEncoding.EncodeToString([]byte(raw)), "OpenVPNPortList_str": ""}})
				}))
				defer server.Close()
				connectAuditServer(t, a, server, 1)
				a.setWireguardPort(1, 51820)
				a.setOpenVPNPorts(1, "1194")
				if failure == "quota" {
					a.saveQuota(1, "hub", "DEFAULT", "", 100, "total", true)
					execute(a.db, `UPDATE TrafficQuota SET RestoreState='{"Online_bool":true}' WHERE ServerID=1`)
				}
				status, out, _ := call(t, a, "PUT", "/api/v1/server/"+protocol, M{"wireguard_port": 51821, "OpenVPNPortList_str": "1195", "confirm_restart": true}, token)
				if status < 400 {
					t.Fatal("failed update reported success", status, out)
				}
				if a.savedWireguardPort(1) != 51820 || a.savedOpenVPNPorts(1) != "1194" {
					t.Fatal("failed update changed saved ports")
				}
				if failure == "quota" && writes.Load() != 0 {
					t.Fatal("blocked quota allowed a configuration write")
				}
			})
		}
	}
}

func TestSecretSettingsMigrateWithoutReencryptingFailures(t *testing.T) {
	a := testApp(t)
	a.set("cf_dns_token", "legacy-plaintext-token")
	legacyCipher := a.encrypt("legacy-ssh-password")
	a.set("cert_ssh_password", legacyCipher)
	a.migrateSecretSettings()
	if a.secretSetting("cf_dns_token") != "legacy-plaintext-token" || a.secretSetting("cert_ssh_password") != "legacy-ssh-password" {
		t.Fatal("legacy secrets did not migrate")
	}
	before := str(a.setting("cert_ssh_password"))
	a.migrateSecretSettings()
	if str(a.setting("cert_ssh_password")) != before {
		t.Fatal("migration rewrote an already encrypted secret")
	}
	key := a.encryptionKey
	a.encryptionKey = "incorrect-key"
	err := recoverAsError(func() error { a.secretSetting("cert_ssh_password"); return nil })
	if err == nil || str(a.setting("cert_ssh_password")) != before {
		t.Fatal("decryption failure was hidden or rewrote the ciphertext")
	}
	a.encryptionKey = key
	if a.secretSetting("cert_ssh_password") != "legacy-ssh-password" {
		t.Fatal("restoring the key could not recover the original secret")
	}
	a.set("cf_dns_token", "not-yet-migrated")
	a.set("cert_ssh_password", legacyCipher)
	a.encryptionKey = "incorrect-key"
	err = recoverAsError(func() error { a.migrateSecretSettings(); return nil })
	if err == nil || str(a.setting("cert_ssh_password")) != legacyCipher || str(a.setting("cf_dns_token")) != "not-yet-migrated" {
		t.Fatal("failed legacy migration partially rewrote stored secrets")
	}
	a.encryptionKey = key
}

func TestInvalidDomainUpdateIsAtomic(t *testing.T) {
	for _, invalid := range []M{
		{"cert_ssh_key": "invalid-private-key"},
		{"cert_renew_hook": "/etc/passwd"},
		{"cert_push_server_id": -1},
		{"cert_push_server_id": 999999},
		{"cert_ssh_host": "invalid host"},
	} {
		a := testApp(t)
		token := admin(t, a)
		a.set("domain", "example.com")
		a.set("domain_only", true)
		a.setSecretSetting("cf_dns_token", "original-token")
		before := str(a.setting("cf_dns_token"))
		body := clone(invalid)
		body["domain"], body["cf_dns_token"] = "new.example.com", "replacement-token"
		status, out, _ := call(t, a, "PUT", "/api/v1/system/domain", body, token)
		if status < 400 {
			t.Fatal("invalid settings accepted", invalid, status, out)
		}
		if a.setting("domain") != "example.com" || !boolean(a.setting("domain_only")) || a.setting("cf_dns_token") != before {
			t.Fatal("validation failure partially saved settings", invalid)
		}
	}
	a := testApp(t)
	token := admin(t, a)
	a.set("domain", "example.com")
	execute(a.db, `CREATE TRIGGER reject_domain_setting BEFORE INSERT ON Setting WHEN NEW.SettingKey='cert_auto_push' BEGIN SELECT RAISE(ABORT,'simulated write failure'); END`)
	status, _, _ := call(t, a, "PUT", "/api/v1/system/domain", M{"domain": "new.example.com", "cert_auto_push": false}, token)
	if status != 500 || a.setting("domain") != "example.com" {
		t.Fatal("database failure did not roll back all settings", status)
	}
}

func TestBackupIntervalsAreValidatedBeforeSaving(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	for _, interval := range []any{0, 1, 48, 6.5, "invalid"} {
		status, _, _ := call(t, a, "PUT", "/api/v1/server/backup-settings", M{"enabled": false, "interval_hours": interval}, token)
		if status != 422 || !boolean(a.setting("backup_enabled")) || integer(a.setting("backup_interval_hours")) != 24 {
			t.Fatal("invalid interval was accepted or partially saved", interval, status)
		}
	}
	at := time.Date(2026, 11, 20, 3, 30, 0, 0, time.UTC)
	if shouldRunBackup(true, 48, "03:00", at.Add(-24*time.Hour).Format(time.RFC3339), at) {
		t.Fatal("legacy 48-hour schedule ran after only 24 hours")
	}
	if !shouldRunBackup(true, 48, "03:00", at.Add(-48*time.Hour).Format(time.RFC3339), at) {
		t.Fatal("legacy 48-hour schedule did not run when due")
	}
}

func TestSessionDetailFetchesNATTableOnce(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		if req["method"] == "EnumNAT" {
			calls.Add(1)
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{
			"IpTable":  []M{{"SessionName_str": "SID-ALICE", "IpAddress_ip": "192.168.30.2"}},
			"NatTable": []M{{"SrcIp_ip": "192.168.30.2", "Protocol_u32": 0}},
		}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	out := a.enrichSessionStatus(t.Context(), "VPN", "SID-ALICE", M{})
	if calls.Load() != 1 || integer(out["ActiveNatTcp_u32"]) != 1 {
		t.Fatal("NAT lookup repeated or changed the result", calls.Load(), out)
	}
}

func TestRejectedConfigRestoreDoesNotChangeTrust(t *testing.T) {
	for _, invalidCertificate := range []bool{false, true} {
		t.Run(fmt.Sprint("invalidCertificate=", invalidCertificate), func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			var writes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req M
				check(json.NewDecoder(r.Body).Decode(&req))
				if req["method"] == "SetConfig" {
					writes.Add(1)
					w.WriteHeader(503)
					return
				}
				writeJSON(w, 200, M{"id": req["id"], "result": M{}})
			}))
			defer server.Close()
			connectAuditServer(t, a, server, 1)
			a.rpc(t.Context(), "GetServerInfo", nil)
			before := str(one(a.db, `SELECT Fingerprint FROM RPCTrust`)["Fingerprint"])
			cert, _, _ := makeTestCert(t, "restore.example", "test", false, nil, nil)
			encoded := base64.StdEncoding.EncodeToString(cert.Raw)
			wantStatus := 502
			if invalidCertificate {
				encoded, wantStatus = "invalid-certificate", 422
			}
			raw := "declare root\n{\n declare ServerConfiguration\n {\n byte ServerCert " + encoded + "\n }\n}\n"
			status, _, _ := call(t, a, "PUT", "/api/v1/server/config", M{"config_base64": base64.StdEncoding.EncodeToString([]byte(raw))}, token)
			pin := one(a.db, `SELECT * FROM RPCTrust`)
			if status != wantStatus || pin["Fingerprint"] != before || pin["PendingFingerprint"] != "" || (invalidCertificate && writes.Load() != 0) {
				t.Fatal("rejected restore changed trust or sent invalid configuration", status, pin, writes.Load())
			}
		})
	}
}

func TestConfigRestartDuringResponseBodyIsAcknowledged(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		if req["method"] == "SetConfig" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"id":`))
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			check(err)
			conn.Close()
			return
		}
		writeJSON(w, 200, M{"id": req["id"], "result": M{"FileData_bin": base64.StdEncoding.EncodeToString([]byte(wgTestConfig5x))}})
	}))
	defer server.Close()
	connectAuditServer(t, a, server, 1)
	status, out, _ := call(t, a, "PUT", "/api/v1/server/ddns", M{"enabled": false, "confirm_restart": true}, token)
	if status != 200 || !boolean(out["restarting"]) {
		t.Fatal("post-write response interruption was reported as a failed change", status, out)
	}
	if one(a.db, `SELECT 1 FROM AuditLog WHERE Action='ddns.toggled'`) == nil {
		t.Fatal("acknowledged restart was not audited")
	}
}

func TestLegacyRPCTrustMigrationPreservesPin(t *testing.T) {
	a := testApp(t)
	execute(a.db, `DROP TABLE RPCTrust`)
	execute(a.db, `CREATE TABLE RPCTrust(Host TEXT NOT NULL COLLATE NOCASE,Port INTEGER NOT NULL,Fingerprint TEXT NOT NULL,PRIMARY KEY(Host,Port))`)
	execute(a.db, `INSERT INTO RPCTrust VALUES('vpn.example',5555,'existing-pin')`)
	reopened, err := New(a.cfg)
	check(err)
	defer reopened.Close()
	row := one(reopened.db, `SELECT Fingerprint,PendingFingerprint FROM RPCTrust`)
	if row["Fingerprint"] != "existing-pin" || row["PendingFingerprint"] != "" {
		t.Fatal("trust migration replaced an existing fingerprint", row)
	}
}

func TestConfigRestoreKeepsTrustAcrossDelayedRestart(t *testing.T) {
	for _, droppedReply := range []bool{false, true} {
		t.Run(fmt.Sprint("droppedReply=", droppedReply), func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			var active atomic.Pointer[tls.Certificate]
			var requests atomic.Int32
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var req M
				check(json.NewDecoder(r.Body).Decode(&req))
				if req["method"] == "SetServerCert" {
					p := object(req["params"])
					cert, _ := base64.StdEncoding.DecodeString(str(p["Cert_bin"]))
					key, _ := base64.StdEncoding.DecodeString(str(p["Key_bin"]))
					pair, err := tls.X509KeyPair(cert, key)
					check(err)
					active.Store(&pair)
				}
				if req["method"] == "SetConfig" && droppedReply {
					conn, _, err := w.(http.Hijacker).Hijack()
					check(err)
					conn.Close()
					return
				}
				writeJSON(w, 200, M{"id": req["id"], "result": M{}})
			}))
			server.TLS = &tls.Config{GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
				return &tls.Config{Certificates: []tls.Certificate{*active.Load()}}, nil
			}}
			server.StartTLS()
			defer server.Close()
			original := &server.TLS.Certificates[0]
			active.Store(original)
			connectAuditServer(t, a, server, 1)
			a.rpc(t.Context(), "GetServerInfo", nil)
			a.regenerateServerCertificate(t.Context(), "vpn.example.test")
			replacement := active.Load()
			raw := "declare root\n{\n declare ServerConfiguration\n {\n byte ServerCert " + base64.StdEncoding.EncodeToString(original.Certificate[0]) + "\n }\n}\n"
			status, out, _ := call(t, a, "PUT", "/api/v1/server/config", M{"config_base64": base64.StdEncoding.EncodeToString([]byte(raw))}, token)
			if status != 200 || !boolean(out["restarting"]) {
				t.Fatal("configuration restart was rejected", status, out)
			}
			pin := one(a.db, `SELECT * FROM RPCTrust`)
			if pin["Fingerprint"] != certFingerprint(replacement.Certificate[0]) || pin["PendingFingerprint"] != certFingerprint(original.Certificate[0]) {
				t.Fatal("restore did not retain current trust and record the expected certificate", pin)
			}
			unexpectedCert, unexpectedKey, _ := makeTestCert(t, "unexpected.example", "test", false, nil, nil)
			active.Store(&tls.Certificate{Certificate: [][]byte{unexpectedCert.Raw}, PrivateKey: unexpectedKey})
			untrusted := a.newRPCClient(a.client().host, a.client().port, "secret", "header")
			before := requests.Load()
			_, err := untrusted.Call(t.Context(), "GetServerInfo", nil)
			untrusted.Close()
			if err == nil || requests.Load() != before {
				t.Fatal("unrelated certificate received credentials during pending restore", err)
			}
			active.Store(replacement)
			// A valid old connection can still be made before SoftEther finishes restarting.
			a.rpc(t.Context(), "GetServerInfo", nil)
			reopened, err := New(a.cfg)
			check(err)
			defer reopened.Close()
			active.Store(original)
			reopened.rpc(t.Context(), "GetServerInfo", nil)
			pin = one(a.db, `SELECT * FROM RPCTrust`)
			if pin["Fingerprint"] != certFingerprint(original.Certificate[0]) || pin["PendingFingerprint"] != "" {
				t.Fatal("restored certificate was not promoted after reconnect", pin)
			}
			active.Store(replacement)
			client := reopened.newRPCClient(a.client().host, a.client().port, "secret", "header")
			defer client.Close()
			before = requests.Load()
			_, err = client.Call(context.Background(), "GetServerInfo", nil)
			if err == nil || !strings.Contains(err.Error(), "certificate changed") || requests.Load() != before {
				t.Fatal("obsolete certificate received credentials after restore", err)
			}
		})
	}
}
