package manager

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedSessionReportRows(t *testing.T, a *App) {
	t.Helper()
	execute(a.db, `INSERT INTO "IPGeoCache"("IP","Country","Region","City","ISP","Location","Full","UpdatedDate") VALUES(?,?,?,?,?,?,?,?)`,
		"198.51.100.10", "中国", "上海", "上海", "中国电信", "上海", "上海 中国电信", now())
	execute(a.db, `INSERT INTO "IPGeoCache"("IP","Country","Region","City","ISP","Location","Full","UpdatedDate") VALUES(?,?,?,?,?,?,?,?)`,
		"203.0.113.20", "日本", "东京", "东京", "NTT", "日本 东京", "日本 东京 NTT", now())

	rows := []struct {
		serverID                         int64
		hub, user, key, cip, vip, host   string
		started, ended, seen             string
		total, pkts, down, up            int64
	}{
		{1, "DEFAULT", "alice", "k1", "198.51.100.10", "10.0.0.2", "alice-mac", "2026-11-10T08:00:00Z", "2026-11-10T10:30:00Z", "2026-11-10T10:30:00Z", 3000, 30, 2000, 1000},
		{1, "OFFICE", "bob_100%", "k2", "203.0.113.20", "10.1.0.5", "=CMD()", "2026-11-18T12:00:00Z", "", "2026-11-18T12:15:00Z", 5000, 50, 3500, 1500},
		{1, "DEFAULT", "charlie", "k3", "192.168.1.99", "10.0.0.9", "iphone", "2026-11-19T01:00:00Z", "2026-11-19T01:45:00Z", "2026-11-19T01:45:00Z", 1200, 12, 800, 400},
	}
	for _, r := range rows {
		var ended any
		if r.ended != "" {
			ended = r.ended
		}
		execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.serverID, r.hub, r.user, r.key, r.cip, r.vip, r.host, r.started, ended, r.seen, r.total, r.pkts, r.down, r.up)
	}
}

func TestSessionReportUsesTimeRangeIndex(t *testing.T) {
	a := testApp(t)
	plan := query(a.db, `EXPLAIN QUERY PLAN SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=1 AND julianday(StartedDate)>=julianday('2026-11-01T00:00:00Z') ORDER BY julianday(StartedDate) DESC, VpnSessionSampleID DESC`)
	if !strings.Contains(str(plan), "IX_VpnSessionSample_ServerStarted") || strings.Contains(str(plan), "TEMP B-TREE") {
		t.Fatalf("session report query did not use IX_VpnSessionSample_ServerStarted index cleanly: %v", plan)
	}
}

func TestSessionReportFilteringAndGeo(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	seedSessionReportRows(t, a)

	// Unfiltered query returns all 3 sessions ordered newest first with summary & hubs.
	code, body, _ := call(t, a, "GET", "/api/v1/server/session-history?limit=10&offset=0", nil, token)
	if code != 200 {
		t.Fatalf("status=%d body=%v", code, body)
	}
	if int(integer(body["total"])) != 3 {
		t.Fatalf("expected total=3, got %v", body["total"])
	}
	items := list(body["items"])
	if len(items) != 3 || str(items[0]["username"]) != "charlie" || str(items[2]["username"]) != "alice" {
		t.Fatalf("unexpected ordering/items: %v", items)
	}
	if str(items[2]["ip_location"]) != "上海" || str(items[2]["ip_isp"]) != "中国电信" {
		t.Fatalf("expected GeoIP enrichment for alice, got %v %v", items[2]["ip_location"], items[2]["ip_isp"])
	}
	if str(items[0]["ip_location"]) != "局域网/内网" {
		t.Fatalf("expected private IP location for charlie, got %v", items[0]["ip_location"])
	}

	// Time filter narrows to Nov 18..Nov 19.
	code, body, _ = call(t, a, "GET", "/api/v1/server/session-history?from=2026-11-18T00:00:00Z&to=2026-11-19T23:59:59Z", nil, token)
	if code != 200 || int(integer(body["total"])) != 2 {
		t.Fatalf("time filter failed: code=%d body=%v", code, body)
	}
	summary, _ := body["summary"].(M)
	if summary == nil || integer(summary["active_sessions"]) != 1 || integer(summary["download_bytes"]) != 4300 {
		t.Fatalf("unexpected summary: %v", summary)
	}

	// Status=active + escaped LIKE wildcard search ("100%").
	code, body, _ = call(t, a, "GET", "/api/v1/server/session-history?status=active&q=100%25", nil, token)
	if code != 200 || int(integer(body["total"])) != 1 || str(list(body["items"])[0]["username"]) != "bob_100%" {
		t.Fatalf("wildcard search failed: code=%d body=%v", code, body)
	}

	// Hub filter.
	code, body, _ = call(t, a, "GET", "/api/v1/server/session-history?hub=OFFICE", nil, token)
	if code != 200 || int(integer(body["total"])) != 1 || str(list(body["items"])[0]["hub"]) != "OFFICE" {
		t.Fatalf("hub filter failed: code=%d body=%v", code, body)
	}

	// Invalid time / status returns 422.
	code, _, _ = call(t, a, "GET", "/api/v1/server/session-history?from=not-a-date", nil, token)
	if code != 422 {
		t.Fatalf("expected 422 for invalid time, got %d", code)
	}
	code, _, _ = call(t, a, "GET", "/api/v1/server/session-history?status=bogus", nil, token)
	if code != 422 {
		t.Fatalf("expected 422 for invalid status, got %d", code)
	}
}

func TestSessionReportExportCSV(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	seedSessionReportRows(t, a)

	req := httptest.NewRequest("GET", "/api/v1/server/session-history/export?lang=zh-CN&tz_offset=-480&from=2026-11-18T00:00:00Z", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("export status=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	raw := rec.Body.Bytes()
	if !bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		t.Fatalf("expected UTF-8 BOM prefix on CSV export")
	}
	records, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 3 { // header + 2 rows (charlie + bob_100%)
		t.Fatalf("expected 3 CSV rows, got %d: %v", len(records), records)
	}
	if records[0][0] != "会话 ID" || records[0][4] != "上线时间" || records[0][9] != "IP 属地" {
		t.Fatalf("unexpected Chinese CSV headers: %v", records[0])
	}
	// Charlie (2026-11-19T01:00:00Z) in UTC+8 (tz_offset=-480) -> 2026-11-19 09:00:00
	if records[1][1] != "charlie" || records[1][4] != "2026-11-19 09:00:00" || records[1][9] != "局域网/内网" {
		t.Fatalf("unexpected row 1: %v", records[1])
	}
	// Bob's hostname "=CMD()" must be neutralized against CSV formula injection.
	if records[2][1] != "bob_100%" || records[2][3] != "在线中" || records[2][12] != "'=CMD()" {
		t.Fatalf("unexpected row 2: %v", records[2])
	}
}

func TestIsClientSessionRPC(t *testing.T) {
	// Normal client sessions
	if !isClientSessionRPC(M{"Username_str": "sean", "Name_str": "SID-SEAN-1"}) {
		t.Fatalf("expected real user sean to be client session")
	}
	if !isClientSessionRPC(M{"Username_str": "alice", "Name_str": "SID-ALICE-123", "ClientIP_ip": "1.2.3.4"}) {
		t.Fatalf("expected real user alice to be client session")
	}

	// System flag sessions
	if isClientSessionRPC(M{"Username_str": "SecureNAT", "SecureNATMode_bool": true}) {
		t.Fatalf("SecureNAT with SecureNATMode_bool must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "Local Bridge", "BridgeMode_bool": true}) {
		t.Fatalf("Bridge with BridgeMode_bool must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "LINK_HUB", "LinkMode_bool": true}) {
		t.Fatalf("Link with LinkMode_bool must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "L3_ROUTER", "Layer3Mode_bool": true}) {
		t.Fatalf("Layer3 with Layer3Mode_bool must not be client session")
	}

	// Flag missing but internal username/name
	if isClientSessionRPC(M{"Username_str": "SecureNAT", "Name_str": "SID-SECURENAT-1"}) {
		t.Fatalf("SecureNAT username must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "securenat", "Name_str": "SID-1"}) {
		t.Fatalf("case-insensitive securenat username must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "Local Bridge", "Name_str": "SID-LOCAL-BRIDGE-1"}) {
		t.Fatalf("Local Bridge username must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "SID-LOCAL-BRIDGE-2", "Name_str": "SID-LOCAL-BRIDGE-2"}) {
		t.Fatalf("SID-LOCAL-BRIDGE username must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "LINK_TO_HQ", "Name_str": "SID-LINK-1"}) {
		t.Fatalf("LINK_ username must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "user", "Name_str": "SID-SECURENAT-5"}) {
		t.Fatalf("SID-SECURENAT name must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "user", "Name_str": "SID-LOCAL-BRIDGE-3"}) {
		t.Fatalf("SID-LOCAL-BRIDGE name must not be client session")
	}
	if isClientSessionRPC(M{"Username_str": "user", "Name_str": "SID-L3-1"}) {
		t.Fatalf("SID-L3 name must not be client session")
	}
}

func TestSessionReportExcludesNonClientSessions(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// Seed client sessions and various system/internal sessions
	execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		1, "DEFAULT", "sean", "k-sean", "1.1.1.1", "10.0.0.1", "sean-phone", "2026-11-19T10:00:00Z", "", "2026-11-19T11:00:00Z", 1000, 10, 600, 400)
	execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		1, "DEFAULT", "SecureNAT", "k-snat", "0.0.0.0", "192.168.30.1", "", "2026-11-02T01:25:23Z", "2026-11-15T22:35:06Z", "2026-11-15T22:35:06Z", 5000, 50, 4000, 1000)
	execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		1, "DEFAULT", "Local Bridge", "k-bridge", "0.0.0.0", "", "", "2026-11-01T00:00:00Z", "", "2026-11-19T11:00:00Z", 2000, 20, 1000, 1000)
	execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		1, "DEFAULT", "LINK_TO_BRANCH", "k-link", "2.2.2.2", "", "", "2026-11-05T00:00:00Z", "", "2026-11-19T11:00:00Z", 3000, 30, 1500, 1500)
	execute(a.db, `INSERT INTO "VpnSessionSample"("ServerID","HubName","UserName","SessionKey","ClientIP","VirtualIP","ClientHostname","StartedDate","EndedDate","LastSeenDate","BytesTotal","PacketsTotal","DownloadBytes","UploadBytes") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		1, "DEFAULT", "SID-LOCAL-BRIDGE-1", "k-bridge-id", "0.0.0.0", "", "", "2026-11-01T00:00:00Z", "", "2026-11-19T11:00:00Z", 2000, 20, 1000, 1000)

	// API session history: must only return sean
	code, body, _ := call(t, a, "GET", "/api/v1/server/session-history", nil, token)
	if code != 200 {
		t.Fatalf("session-history status=%d body=%v", code, body)
	}
	if total := integer(body["total"]); total != 1 {
		t.Fatalf("expected total=1 (only sean), got %d", total)
	}
	items := list(body["items"])
	if len(items) != 1 || str(items[0]["username"]) != "sean" {
		t.Fatalf("expected items=[sean], got %v", items)
	}
	summary, _ := body["summary"].(M)
	if summary == nil || integer(summary["unique_users"]) != 1 || integer(summary["total_sessions"]) != 1 {
		t.Fatalf("expected unique_users=1, total_sessions=1, got %v", summary)
	}

	// Hub session history: must only return sean
	code, _, recHub := call(t, a, "GET", "/api/v1/hubs/DEFAULT/session-history", nil, token)
	if code != 200 {
		t.Fatalf("hub session-history status=%d body=%s", code, recHub.Body.String())
	}
	var hItems []M
	if err := json.Unmarshal(recHub.Body.Bytes(), &hItems); err != nil {
		t.Fatalf("unmarshal hub session-history: %v", err)
	}
	if len(hItems) != 1 || str(hItems[0]["username"]) != "sean" {
		t.Fatalf("expected hub history items=[sean], got %v", hItems)
	}

	// CSV Export: must only contain sean
	req := httptest.NewRequest("GET", "/api/v1/server/session-history/export", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export code=%d", rec.Code)
	}
	records, err := csv.NewReader(bytes.NewReader(rec.Body.Bytes()[3:])).ReadAll()
	if err != nil {
		t.Fatalf("parse CSV: %v", err)
	}
	if len(records) != 2 { // header + 1 row (sean)
		t.Fatalf("expected 2 CSV rows (header + sean), got %d: %v", len(records), records)
	}
	if records[1][1] != "sean" {
		t.Fatalf("expected CSV row 1 username=sean, got %s", records[1][1])
	}

	// prune() should delete the non-client rows from SQLite
	a.prune()
	nonClientCount := integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionSample WHERE LOWER(UserName) IN ('securenat', 'local bridge') OR LOWER(UserName) LIKE 'sid-local-bridge%' OR LOWER(UserName) LIKE 'link_%'`)["n"])
	if nonClientCount != 0 {
		t.Fatalf("expected 0 non-client rows in database after prune, got %d", nonClientCount)
	}
	clientCount := integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionSample WHERE UserName='sean'`)["n"])
	if clientCount != 1 {
		t.Fatalf("expected sean to still be in database after prune, got %d", clientCount)
	}
}
