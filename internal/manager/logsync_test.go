package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSampleHubSessionsSkipsGetSessionStatusWhenTrafficUnchangedAndPersistsMeta(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	ctx := context.Background()

	var statusCalls atomic.Int32
	var mu sync.Mutex
	pktSize := int64(1000)
	downBytes, upBytes := int64(600), int64(400)
	includeUserInEnum := true

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "EnumHub":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"HubList": []M{{"HubName_str": "DEFAULT", "Online_bool": true}}}})
		case "EnumSession":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"SessionList": []M{{
				"Name_str":       "SID-ALICE-1",
				"Username_str":   "alice",
				"UniqueId_bin":   "alice-uid-1",
				"ClientIP_ip":    "192.168.10.25",
				"CreatedTime_dt": time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339),
				"PacketSize_u64": pktSize,
				"PacketNum_u64":  10,
			}}}})
		case "EnumIpTable":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"IpTable": []M{{"SessionName_str": "SID-ALICE-1", "IpAddress_ip": "10.0.0.5"}}}})
		case "EnumDHCP":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"DhcpTable": []M{}}})
		case "EnumUser":
			if includeUserInEnum {
				writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{{"Name_str": "alice", "Realname_utf": "Alice Chen", "Note_utf": "Engineering", "NumLogin_u32": 1}}}})
			} else {
				writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{}}})
			}
		case "GetSessionStatus":
			statusCalls.Add(1)
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"Send.UnicastBytes_u64": downBytes,
				"Recv.UnicastBytes_u64": upBytes,
				"ClientHostname_str":    "alice-laptop",
				"UnderlayProtocol_str":  "TCP/IP (IPv4) (TLSv1.3)",
				"ServerPort2_u32":       443,
				"ClientPort_u32":        52100,
			}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	// Poll 1: fetches GetSessionStatus once and persists user/protocol/geo metadata.
	a.sampleSessions(ctx)
	if statusCalls.Load() != 1 {
		t.Fatalf("expected 1 GetSessionStatus call on first poll, got %d", statusCalls.Load())
	}

	// Poll 2: PacketSize_u64 is unchanged -> must skip GetSessionStatus RPC!
	a.sampleSessions(ctx)
	if statusCalls.Load() != 1 {
		t.Fatalf("expected unchanged session to skip GetSessionStatus RPC, got %d calls", statusCalls.Load())
	}
	if n := integer(one(a.db, `SELECT COUNT(*) n FROM VpnSessionTrafficSample`)["n"]); n != 2 {
		t.Fatalf("expected continuous 2 session traffic points even when RPC skipped, got %d", n)
	}

	// Poll 3: PacketSize_u64 increases -> must fetch GetSessionStatus again.
	mu.Lock()
	pktSize = 2500
	downBytes, upBytes = 1500, 1000
	mu.Unlock()
	a.sampleSessions(ctx)
	if statusCalls.Load() != 2 {
		t.Fatalf("expected changed session traffic to call GetSessionStatus, got %d calls", statusCalls.Load())
	}

	// Delete user from EnumUser and clear userMeta cache: persisted RealName/Note must remain!
	mu.Lock()
	includeUserInEnum = false
	mu.Unlock()
	a.invalidateUserMetaCache(1, "DEFAULT")

	code, body, _ := call(t, a, "GET", "/api/v1/server/session-history", nil, token)
	if code != 200 {
		t.Fatalf("session-history status=%d body=%v", code, body)
	}
	items := list(body["items"])
	if len(items) != 1 || str(items[0]["real_name"]) != "Alice Chen" || str(items[0]["note"]) != "Engineering" || str(items[0]["protocol"]) != "SoftEther SSL-VPN" {
		t.Fatalf("expected persisted session metadata to survive user deletion, got %v", items)
	}
}

func TestSecurityLogSyncBackfillsShortSessionsAndExactTermination(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	ctx := context.Background()
	execute(a.db, `INSERT INTO "IPGeoCache"("IP","Country","Region","City","ISP","Location","Full","UpdatedDate") VALUES(?,?,?,?,?,?,?,?)`,
		"198.51.100.55", "测试", "本地", "本地", "TestISP", "测试 本地", "测试 本地 TestISP", now())

	today := time.Now().UTC().Format("20060102")
	logPath := fmt.Sprintf("security_log/DEFAULT/sec_%s.log", today)

	var mu sync.Mutex
	var logBuf string
	var readCalls atomic.Int32

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "GetServerStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"CurrentTime_dt": time.Now().UTC().Format(time.RFC3339)}})
		case "EnumLogFile":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"LogFiles": []M{{
				"FilePath_str":   logPath,
				"ServerName_str": "vpn1",
				"FileSize_u64":   len(logBuf),
			}}}})
		case "ReadLogFile":
			readCalls.Add(1)
			p := object(req["params"])
			off := int(integer(p["Offset_u32"]))
			if off > len(logBuf) {
				off = len(logBuf)
			}
			chunk := logBuf[off:]
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"Buffer_bin": base64.StdEncoding.EncodeToString([]byte(chunk)),
			}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	// Seed an existing polling-captured session for alice whose polling ended slightly early with partial bytes.
	tStart := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	tSeen := tStart.Add(2 * time.Minute)
	tExactEnd := tSeen.Add(45 * time.Second)
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,StartedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs) VALUES(1,'DEFAULT','alice','sess:SID-ALICE-1@1','192.168.1.10',?,?,500,300,200,?)`,
		tStart.Format(time.RFC3339), tSeen.Format(time.RFC3339), tStart.Unix())

	// Seed a synthetic short:% row for bob that the log entry should replace cleanly without duplication.
	tBobStart := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	tBobEnd := tBobStart.Add(15 * time.Second)
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,StartedDate,EndedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,EndedTs) VALUES(1,'DEFAULT','bob','short:bob:1',?,?,?,0,0,0,?,?)`,
		tBobStart.Format(time.RFC3339), tBobEnd.Format(time.RFC3339), tBobEnd.Format(time.RFC3339), tBobStart.Unix(), tBobEnd.Unix())

	fmtLogTs := func(ts time.Time) string {
		return ts.UTC().Format("2006-01-02 15:04:05.000")
	}

	mu.Lock()
	logBuf = strings.Join([]string{
		fmt.Sprintf(`%s Connection "CID-1": The IP address of the client is 192.168.1.10, and the port number is 41000.`, fmtLogTs(tStart)),
		fmt.Sprintf(`%s Connection "CID-1": User authentication succeeded. User name: "alice"`, fmtLogTs(tStart)),
		fmt.Sprintf(`%s Connection "CID-1": A new session "SID-ALICE-1" has been created. (IP address: 192.168.1.10, Port number: 41000, Physical protocol: "Standard TCP/IP (IPv4)")`, fmtLogTs(tStart)),
		fmt.Sprintf(`%s Session "SID-ALICE-1": The session has been terminated. The statistical information is as follows: Total outgoing data size: 1800 bytes, Total incoming data size: 900 bytes, Total outgoing unicast packets: 12 packets, Total incoming unicast packets: 8 packets.`, fmtLogTs(tExactEnd)),
		fmt.Sprintf(`%s Connection "CID-1" terminated by the cause "The VPN session has been deleted." (code 11).`, fmtLogTs(tExactEnd)),
		fmt.Sprintf(`%s Connection "CID-2": Connection started from 198.51.100.55 (port 51234).`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Connection "CID-2": Successfully authenticated as user "bob"`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Connection "CID-2": A new session "SID-BOB-2" has been created. (IP address: 198.51.100.55, Port number: 51234, Physical protocol: "OpenVPN")`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Session "SID-BOB-2": The session has been terminated. The statistical information is as follows: Total outgoing data size: 12000 bytes, Total incoming data size: 3400 bytes.`, fmtLogTs(tBobEnd)),
		fmt.Sprintf(`%s Connection "CID-2" terminated by the cause "No error." (code 0).`, fmtLogTs(tBobEnd)),
		fmt.Sprintf(`%s Session "SID-SECURENAT-1": The session has been terminated. The statistical information is as follows: Total outgoing data size: 99999 bytes, Total incoming data size: 99999 bytes.`, fmtLogTs(tBobEnd)),
		"",
	}, "\n")
	mu.Unlock()

	a.syncSessionLogs(ctx, 1)

	// Calling again when file size is unchanged must not call ReadLogFile!
	callsBefore := readCalls.Load()
	a.syncSessionLogs(ctx, 1)
	if readCalls.Load() != callsBefore {
		t.Fatalf("expected unchanged log file to skip ReadLogFile, calls went from %d to %d", callsBefore, readCalls.Load())
	}

	code, body, _ := call(t, a, "GET", "/api/v1/server/session-history", nil, token)
	if code != 200 || integer(body["total"]) != 2 {
		t.Fatalf("expected 2 client sessions (alice updated + bob backfilled, SecureNAT excluded), got code=%d body=%v", code, body)
	}
	items := list(body["items"])
	var alice, bob M
	for _, it := range items {
		switch str(it["username"]) {
		case "alice":
			alice = it
		case "bob":
			bob = it
		}
	}
	if alice == nil || integer(alice["download_bytes"]) != 1800 || integer(alice["upload_bytes"]) != 900 || integer(alice["bytes_total"]) != 2700 || integer(alice["packets_total"]) != 20 {
		t.Fatalf("alice session was not calibrated with exact final log stats: %v", alice)
	}
	if !strings.Contains(str(alice["disconnect_reason"]), "code 11") || parseTime(str(alice["ended_date"])).Unix() != tExactEnd.Unix() {
		t.Fatalf("alice session missing exact end time or disconnect reason: %v", alice)
	}
	if bob == nil || str(bob["client_ip"]) != "198.51.100.55" || integer(bob["client_port"]) != 51234 || str(bob["protocol"]) != "OpenVPN" || integer(bob["download_bytes"]) != 12000 || integer(bob["upload_bytes"]) != 3400 {
		t.Fatalf("bob short session was not backfilled cleanly from security log: %v", bob)
	}
}
