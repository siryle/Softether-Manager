package manager

import (
	"context"
	"testing"
)

func TestSessionSamplingGapPreservesCounters(t *testing.T) {
	for _, gap := range []string{"rpc failure", "sampling disabled"} {
		t.Run(gap, func(t *testing.T) {
			a := testApp(t)
			fake := attachSoftEther(t, a)
			a.sampleSessions(context.Background())
			before := one(a.db, `SELECT * FROM VpnSessionSample`)
			fake.mu.Lock()
			fake.send, fake.recv = 700, 450
			fake.failSessionStatus = gap == "rpc failure"
			fake.mu.Unlock()
			if gap == "sampling disabled" {
				a.set("session_traffic_enabled", false)
			}
			a.sampleSessions(context.Background())
			after := one(a.db, `SELECT * FROM VpnSessionSample`)
			if after["DownloadBytes"] != before["DownloadBytes"] || after["UploadBytes"] != before["UploadBytes"] {
				t.Fatalf("a sampling gap erased the last known counters: %v", after)
			}
			if integer(after["BytesTotal"]) != 1150 || after["EndedDate"] != nil {
				t.Fatal("session history must still refresh during a traffic sampling gap", after)
			}
			if count := integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionTrafficSample`)["n"]); count != 1 {
				t.Fatalf("the gap inserted a false traffic point: %d samples", count)
			}
			fake.mu.Lock()
			fake.failSessionStatus = false
			fake.mu.Unlock()
			a.set("session_traffic_enabled", true)
			a.sampleSessions(context.Background())
			rows := query(a.db, `SELECT * FROM VpnSessionTrafficSample ORDER BY VpnSessionTrafficSampleID`)
			out := usage(rows, 1, "UploadBytes", "DownloadBytes", false)
			if integer(out["total_send"]) != 50 || integer(out["total_recv"]) != 100 {
				t.Fatal("resuming sampling counted old traffic twice", out)
			}
		})
	}
}

func TestConnectionChangesCannotDiscardQuotaRestoreState(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	blockedID, _ := managedFake(t, a, "blocked")
	otherID, _ := managedFake(t, a, "other")
	a.saveQuota(blockedID, "user", "VPN", "Alice", 100, "total", true)
	a.quotaTick(context.Background())
	original := a.serverRow(blockedID)
	if str(a.quotaRow(blockedID, "user", "VPN", "Alice")["RestoreState"]) == "" {
		t.Fatal("the test server was not blocked")
	}
	// The remote block may have succeeded before its local completion marker was written.
	execute(a.db, `UPDATE TrafficQuota SET EnforcedDate=NULL WHERE ServerID=?`, blockedID)
	for _, tc := range []struct {
		method, path string
		body         M
	}{
		{"DELETE", "/api/v1/servers/" + str(blockedID), nil},
		{"PUT", "/api/v1/servers/" + str(blockedID), M{"host": "replacement.example", "skip_test": true}},
		{"PUT", "/api/v1/servers/" + str(blockedID), M{"is_enabled": false}},
	} {
		status, out, _ := callServer(t, a, otherID, tc.method, tc.path, tc.body, token)
		if status != 409 {
			t.Fatalf("discarded an outstanding quota restoration: %s %s: %d %v", tc.method, tc.path, status, out)
		}
	}
	status, out, _ := callServer(t, a, blockedID, "PUT", "/api/v1/server/connection", M{"host": "replacement.example", "port": 443, "skip_test": true}, token)
	if status != 409 {
		t.Fatal("the connection endpoint discarded quota restoration state", status, out)
	}
	if a.serverRow(blockedID)["Host"] != original["Host"] {
		t.Fatal("a rejected change moved the connection")
	}
	status, out, _ = callServer(t, a, otherID, "PUT", "/api/v1/servers/"+str(blockedID), M{"password": "repaired password", "skip_test": true}, token)
	if status != 200 {
		t.Fatal("credential repairs must remain possible while a quota awaits restoration", status, out)
	}
	status, out, _ = callServer(t, a, blockedID, "PUT", "/api/v1/quotas/user/VPN/Alice/enabled", M{"enabled": false}, token)
	if status != 200 || boolean(out["blocked"]) {
		t.Fatal("the operator could not release the quota", status, out)
	}
	status, out, _ = callServer(t, a, otherID, "DELETE", "/api/v1/servers/"+str(blockedID), nil, token)
	if status != 200 {
		t.Fatal("a released connection could not be removed", status, out)
	}
}
