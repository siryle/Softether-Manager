package manager

import (
	"testing"
	"time"
)

func TestServerAnalyticsAndSessionReportAnalytics(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	nowUTC := time.Now().UTC().Truncate(time.Second)
	hourBucket := nowUTC.Unix() - (nowUTC.Unix() % 3600)

	// Seed TrafficRollupHourly for server analytics.
	execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'user','DEFAULT','alice','Alice',?,1000,4000,0,5)`, hourBucket)
	execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'user','OFFICE','bob','Bob',?,2000,9000,0,5)`, hourBucket)
	execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'hub','DEFAULT','','',?,3000,8000,4,5)`, hourBucket)
	execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'hub','OFFICE','','',?,4000,12000,2,5)`, hourBucket)

	// Seed VpnSessionSample rows (including active multi-IP, abnormal disconnect, short session, protocol & location).
	s1 := nowUTC.Add(-2 * time.Hour)
	e1 := s1.Add(40 * time.Second) // short session + abnormal disconnect
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,StartedDate,EndedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,EndedTs,RealName,IpLocation,Protocol,DisconnectReason) VALUES(1,'OFFICE','Bob','k1','203.0.113.10',?,?,?,15000,10000,5000,?,?,'Bob Lin','日本 东京','OpenVPN','Session deleted (code 11)')`,
		s1.Format(time.RFC3339), e1.Format(time.RFC3339), e1.Format(time.RFC3339), s1.Unix(), e1.Unix())

	s2 := nowUTC.Add(-30 * time.Minute)
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,StartedDate,EndedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,RealName,IpLocation,Protocol) VALUES(1,'DEFAULT','Alice','k2','198.51.100.1',?,NULL,?,6000,4000,2000,?,'Alice Chen','上海','WireGuard')`,
		s2.Format(time.RFC3339), nowUTC.Format(time.RFC3339), s2.Unix())
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,StartedDate,EndedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,RealName,IpLocation,Protocol) VALUES(1,'DEFAULT','Alice','k3','198.51.100.2',?,NULL,?,3000,2000,1000,?,'Alice Chen','北京','WireGuard')`,
		s2.Format(time.RFC3339), nowUTC.Format(time.RFC3339), s2.Unix())

	// 1. Server analytics
	code, out, _ := call(t, a, "GET", "/api/v1/server/analytics?hours=24", nil, token)
	if code != 200 {
		t.Fatalf("server analytics status=%d out=%v", code, out)
	}
	topUsers := list(out["top_users"])
	if len(topUsers) != 2 || str(topUsers[0]["username"]) != "Bob" || integer(topUsers[0]["total_bytes"]) != 11000 {
		t.Fatalf("unexpected top_users: %v", topUsers)
	}
	hubTotals := list(out["hub_totals"])
	if len(hubTotals) != 2 || str(hubTotals[0]["hub"]) != "OFFICE" {
		t.Fatalf("unexpected hub_totals: %v", hubTotals)
	}
	protoCounts := list(out["protocol_counts"])
	if len(protoCounts) != 1 || str(protoCounts[0]["protocol"]) != "WireGuard" || integer(protoCounts[0]["count"]) != 2 {
		t.Fatalf("unexpected protocol_counts: %v", protoCounts)
	}

	// 2. Hub-scoped server analytics
	code, out, _ = call(t, a, "GET", "/api/v1/server/analytics?hours=24&hub=DEFAULT", nil, token)
	if code != 200 || len(list(out["top_users"])) != 1 || str(list(out["top_users"])[0]["username"]) != "Alice" {
		t.Fatalf("hub-scoped analytics failed: code=%d out=%v", code, out)
	}

	// 3. Session report analytics
	code, out, _ = call(t, a, "GET", "/api/v1/server/session-history/analytics?tz_offset=0", nil, token)
	if code != 200 {
		t.Fatalf("session analytics status=%d out=%v", code, out)
	}
	anom := object(out["anomalies"])
	if integer(anom["abnormal_disconnects"]) != 1 || integer(anom["short_sessions"]) != 1 || integer(anom["concurrent_multi_ip"]) != 1 {
		t.Fatalf("unexpected anomalies: %v", anom)
	}
	if len(list(out["buckets"])) == 0 || len(list(out["top_users"])) != 2 || len(list(out["protocols"])) != 2 {
		t.Fatalf("unexpected session analytics breakdowns: %v", out)
	}

	// 4. Protocol filter & bytes sort on session-history
	code, out, _ = call(t, a, "GET", "/api/v1/server/session-history?protocol=OpenVPN&sort=bytes", nil, token)
	if code != 200 || integer(out["total"]) != 1 || str(list(out["items"])[0]["username"]) != "Bob" {
		t.Fatalf("protocol filter / sort failed: code=%d out=%v", code, out)
	}
}
