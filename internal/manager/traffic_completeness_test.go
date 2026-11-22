package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSampleTrafficPreservesPostRestartAndDowntimeGapTotalsAndRollup(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	ctx := context.Background()

	var mu sync.Mutex
	startTime := "2026-11-20T00:00:00Z"
	hubSend, hubRecv := int64(10_000), int64(20_000)
	userSend, userRecv := int64(4_000), int64(8_000)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "GetServerStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"StartTime_dt": startTime, "CurrentTime_dt": time.Now().UTC().Format(time.RFC3339)}})
		case "EnumHub":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"HubList": []M{{"HubName_str": "DEFAULT", "Online_bool": true}}}})
		case "GetHubStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"Send.UnicastBytes_u64": hubSend,
				"Recv.UnicastBytes_u64": hubRecv,
				"NumSessions_u32":       1,
				"NumUsers_u32":          1,
			}})
		case "EnumUser":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{{
				"Name_str":                "alice",
				"Ex.Send.UnicastBytes_u64": userSend,
				"Ex.Recv.UnicastBytes_u64": userRecv,
				"NumLogin_u32":            1,
			}}}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	// 1. Baseline observation: must not invent historical lifetime bytes.
	a.sampleTraffic(ctx)

	// 2. Normal interval growth (+5k / +6k hub, +1k / +2k user).
	mu.Lock()
	hubSend, hubRecv = 15_000, 26_000
	userSend, userRecv = 5_000, 10_000
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 3. Simulate a 2-hour panel downtime gap by shifting previous cursor/sample timestamps back 2 hours.
	twoHoursAgo := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	execute(a.db, `UPDATE HubTrafficSample SET SampledDate=?,SampledTs=?`, twoHoursAgo.Format(time.RFC3339), twoHoursAgo.Unix())
	execute(a.db, `UPDATE UserTrafficSample SET SampledDate=?,SampledTs=?`, twoHoursAgo.Format(time.RFC3339), twoHoursAgo.Unix())
	execute(a.db, `UPDATE TrafficCursor SET LastSampledTs=?`, twoHoursAgo.Unix())

	mu.Lock()
	hubSend, hubRecv = 25_000, 40_000 // +10k / +14k across the 2h gap
	userSend, userRecv = 9_000, 16_000 // +4k / +6k across the 2h gap
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 4. Confirmed SoftEther restart (StartTime_dt changed, counters restarted from 0 -> 3k/4k).
	mu.Lock()
	startTime = "2026-11-21T12:00:00Z"
	hubSend, hubRecv = 3_000, 4_000
	userSend, userRecv = 1_500, 2_500
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 5. Transient bogus zero sample while StartTime_dt is unchanged: must NOT drop cursor or break next delta.
	mu.Lock()
	hubSend, hubRecv = 0, 0
	userSend, userRecv = 0, 0
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 6. Resume after bogus zero (+2k / +3k above pre-glitch 3k/4k).
	mu.Lock()
	hubSend, hubRecv = 5_000, 7_000
	userSend, userRecv = 2_500, 4_000
	mu.Unlock()
	a.sampleTraffic(ctx)

	// Expected totals (hub/server traffic aggregates real VPN users, excluding inflated GetHubStatus Local Bridge/SecureNAT counters):
	// User & Server: (1k + 4k + 1.5k + 1k) = 7,500 send; (2k + 6k + 2.5k + 1.5k) = 12,000 recv
	for _, hours := range []string{"24", "168"} {
		code, out, _ := call(t, a, "GET", "/api/v1/server/traffic?hours="+hours, nil, token)
		if code != 200 || integer(out["total_send"]) != 7_500 || integer(out["total_recv"]) != 12_000 {
			t.Fatalf("server traffic (%sh) mismatch: code=%d out=%v", hours, code, out)
		}
		code, out, _ = call(t, a, "GET", "/api/v1/hubs/DEFAULT/traffic?hours="+hours, nil, token)
		if code != 200 || integer(out["total_send"]) != 7_500 || integer(out["total_recv"]) != 12_000 {
			t.Fatalf("hub traffic (%sh) mismatch: code=%d out=%v", hours, code, out)
		}
		code, out, _ = call(t, a, "GET", "/api/v1/hubs/DEFAULT/users/alice/usage?hours="+hours, nil, token)
		if code != 200 || integer(out["total_send"]) != 7_500 || integer(out["total_recv"]) != 12_000 {
			t.Fatalf("user traffic (%sh) mismatch: code=%d out=%v", hours, code, out)
		}
	}
}

func TestSampleTrafficExcludesBridgeInflationHandlesUserDeleteAndRestartRollback(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	ctx := context.Background()

	var mu sync.Mutex
	startTime := "2026-11-20T00:00:00Z"
	users := []M{
		{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": int64(5_000_000_000_000), "Ex.Recv.UnicastBytes_u64": int64(5_000_000_000_000), "NumLogin_u32": 1},
		{"Name_str": "bob", "Ex.Send.UnicastBytes_u64": int64(5_000), "Ex.Recv.UnicastBytes_u64": int64(8_000), "NumLogin_u32": 1},
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "GetServerStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"StartTime_dt": startTime, "CurrentTime_dt": time.Now().UTC().Format(time.RFC3339)}})
		case "EnumHub":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"HubList": []M{{"HubName_str": "DEFAULT", "Online_bool": true}}}})
		case "GetHubStatus":
			// Inflated Local Bridge / LAN counters must never leak into server/hub usage.
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"Send.UnicastBytes_u64": int64(9_000_000_000_000),
				"Recv.UnicastBytes_u64": int64(9_000_000_000_000),
				"NumSessions_u32":       2,
				"NumUsers_u32":          len(users),
			}})
		case "EnumUser":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": users}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	// 1. Baseline observation
	a.sampleTraffic(ctx)

	// 2. Normal growth: alice +500/+1000, bob +1000/+2000 -> total +1500/+3000
	mu.Lock()
	users = []M{
		{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": int64(5_000_000_000_500), "Ex.Recv.UnicastBytes_u64": int64(5_000_000_001_000), "NumLogin_u32": 1},
		{"Name_str": "bob", "Ex.Send.UnicastBytes_u64": int64(6_000), "Ex.Recv.UnicastBytes_u64": int64(10_000), "NumLogin_u32": 1},
	}
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 3. Delete bob while alice grows +300/+700 -> total must not drop or spike
	mu.Lock()
	users = []M{
		{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": int64(5_000_000_000_800), "Ex.Recv.UnicastBytes_u64": int64(5_000_000_001_700), "NumLogin_u32": 1},
	}
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 4. Unclean OpenWrt reboot: StartTime_dt changes and vpn_server.config reloads slightly older 5 TB counter
	mu.Lock()
	startTime = "2026-11-22T06:00:00Z"
	users = []M{
		{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": int64(5_000_000_000_200), "Ex.Recv.UnicastBytes_u64": int64(5_000_000_000_400), "NumLogin_u32": 1},
	}
	mu.Unlock()
	a.sampleTraffic(ctx)

	// 5. Post-reboot normal traffic: alice +400/+900 above re-baselined counter
	mu.Lock()
	users = []M{
		{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": int64(5_000_000_000_600), "Ex.Recv.UnicastBytes_u64": int64(5_000_000_001_300), "NumLogin_u32": 1},
	}
	mu.Unlock()
	a.sampleTraffic(ctx)

	// Expected: (500+1000) + 300 + 0 (rollback re-baseline) + 400 = 2,200 send
	//           (1000+2000) + 700 + 0 (rollback re-baseline) + 900 = 4,600 recv
	code, out, _ := call(t, a, "GET", "/api/v1/server/traffic?hours=24", nil, token)
	if code != 200 || integer(out["total_send"]) != 2_200 || integer(out["total_recv"]) != 4_600 {
		t.Fatalf("unexpected server traffic after delete & rollback: code=%d out=%v", code, out)
	}

	// Also verify sub-cap config rollback (500 MB -> 490 MB on restart) re-baselines without inventing +490 MB.
	prev := trafficCursorState{lastSend: 250 << 20, lastRecv: 250 << 20, serverStartTime: "2026-11-20T00:00:00Z", lastSampledTs: 1000}
	dS, dR, hasDelta, nextC, upd := computeTrafficSampleDelta(prev, true, 245<<20, 245<<20, 1300, "2026-11-22T06:00:00Z")
	if hasDelta || dS != 0 || dR != 0 || !upd || nextC.lastSend != 245<<20 || nextC.lastRecv != 245<<20 {
		t.Fatalf("expected clean re-baseline on 500MB->490MB restart rollback, got dS=%d dR=%d hasDelta=%v upd=%v next=%+v", dS, dR, hasDelta, upd, nextC)
	}
}

func TestTrafficClientScopeMigrationRebuildsHubHistoryFromUsers(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	ts := time.Now().UTC().Truncate(time.Second)
	stamp := ts.Format(time.RFC3339)
	bucket := ts.Unix() - (ts.Unix() % 3600)

	// Seed legacy GetHubStatus-polluted hub rows alongside clean user rows, then clear the migration marker.
	execute(db, `INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,NumSessions,NumUsers,SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta) VALUES(1,'DEFAULT',5000000000000,5030000000000,7,2,?,?,14900000000,14800000000,1)`, stamp, ts.Unix())
	execute(db, `INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,NumLogin,SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta) VALUES(1,'DEFAULT','alice',1000,4000,1,?,?,100,400,1)`, stamp, ts.Unix())
	execute(db, `INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,NumLogin,SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta) VALUES(1,'DEFAULT','bob',2000,6000,1,?,?,200,600,1)`, stamp, ts.Unix())
	execute(db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'hub','DEFAULT','','',?,14900000000,14800000000,7,12)`, bucket)
	execute(db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'user','DEFAULT','alice','alice',?,100,400,0,12)`, bucket)
	execute(db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'user','DEFAULT','bob','bob',?,200,600,0,12)`, bucket)
	execute(db, `INSERT INTO TrafficCursor(ServerID,SubjectType,HubName,UserKey,LastSendBytes,LastRecvBytes,ServerStartTime,LastSampledTs) VALUES(1,'hub','DEFAULT','',5000000000000,5030000000000,'t0',?)`, ts.Unix())
	execute(db, `DELETE FROM Setting WHERE SettingKey='_traffic_client_scope_v1'`)
	check(db.Close())

	migrated, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("reopen migrated DB: %v", err)
	}
	defer migrated.Close()

	hubRow := one(migrated, `SELECT SendBytes,RecvBytes,DeltaSendBytes,DeltaRecvBytes,HasDelta,NumSessions FROM HubTrafficSample WHERE ServerID=1 AND HubName='DEFAULT'`)
	if integer(hubRow["SendBytes"]) != 3000 || integer(hubRow["RecvBytes"]) != 10000 || integer(hubRow["DeltaSendBytes"]) != 300 || integer(hubRow["DeltaRecvBytes"]) != 1000 || integer(hubRow["HasDelta"]) != 1 || integer(hubRow["NumSessions"]) != 7 {
		t.Fatalf("HubTrafficSample not rebuilt cleanly: %v", hubRow)
	}
	rollupRow := one(migrated, `SELECT SendBytes,RecvBytes,MaxSessions FROM TrafficRollupHourly WHERE ServerID=1 AND SubjectType='hub' AND HubName='DEFAULT' AND BucketTs=?`, bucket)
	if integer(rollupRow["SendBytes"]) != 300 || integer(rollupRow["RecvBytes"]) != 1000 || integer(rollupRow["MaxSessions"]) != 7 {
		t.Fatalf("TrafficRollupHourly hub row not rebuilt cleanly: %v", rollupRow)
	}
	if cur := one(migrated, `SELECT 1 FROM TrafficCursor WHERE SubjectType='hub'`); cur != nil {
		t.Fatalf("expected legacy hub TrafficCursor to be removed")
	}
}

func TestTrafficUsageUsesRollupFastPathForLongWindows(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	nowHour := time.Now().UTC().Unix()
	nowHour -= nowHour % 3600

	for i := 0; i < 720; i++ {
		b := nowHour - int64(719-i)*3600
		execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'hub','DEFAULT','','',?,100,200,2,12)`, b)
		execute(a.db, `INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(1,'user','DEFAULT','alice','Alice',?,50,80,0,12)`, b)
	}

	code, out, _ := call(t, a, "GET", "/api/v1/server/traffic?hours=720", nil, token)
	if code != 200 || integer(out["total_send"]) != 72_000 || integer(out["total_recv"]) != 144_000 {
		t.Fatalf("30d server rollup mismatch: code=%d out=%v", code, out)
	}
	if pts := list(out["points"]); len(pts) > 370 || len(pts) < 100 {
		t.Fatalf("expected downsampled points in [100, 370], got %d", len(pts))
	}

	code, out, _ = call(t, a, "GET", "/api/v1/hubs/DEFAULT/users/alice/usage?hours=720", nil, token)
	if code != 200 || integer(out["total_send"]) != 36_000 || integer(out["total_recv"]) != 57_600 {
		t.Fatalf("30d user rollup mismatch: code=%d out=%v", code, out)
	}
}

func TestQuotaTickBatchesEnumUserForMultipleQuotas(t *testing.T) {
	a := testApp(t)
	var enumCalls, getUserCalls atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		switch req["method"] {
		case "EnumUser":
			enumCalls.Add(1)
			writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{
				{"Name_str": "alice", "Ex.Send.UnicastBytes_u64": 100, "Ex.Recv.UnicastBytes_u64": 200},
				{"Name_str": "bob", "Ex.Send.UnicastBytes_u64": 300, "Ex.Recv.UnicastBytes_u64": 400},
				{"Name_str": "carol", "Ex.Send.UnicastBytes_u64": 500, "Ex.Recv.UnicastBytes_u64": 600},
			}}})
		case "GetUser":
			getUserCalls.Add(1)
			writeJSON(w, 200, M{"id": req["id"], "result": M{"Name_str": "unknown", "Send.UnicastBytes_u64": 0, "Recv.UnicastBytes_u64": 0}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	for _, u := range []string{"alice", "bob", "carol"} {
		a.saveQuota(1, "user", "DEFAULT", u, 1<<30, "total", true)
	}
	a.quotaTick(context.Background())

	if enumCalls.Load() != 1 || getUserCalls.Load() != 0 {
		t.Fatalf("expected 1 EnumUser batch call and 0 GetUser calls, got enum=%d getUser=%d", enumCalls.Load(), getUserCalls.Load())
	}
	if row := a.quotaRow(1, "user", "DEFAULT", "bob"); row == nil || integer(row["LastSendBytes"]) != 300 || integer(row["LastRecvBytes"]) != 400 {
		t.Fatalf("expected bob quota counters updated from EnumUser batch, got %v", row)
	}
}
