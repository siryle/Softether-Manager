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
		fmt.Sprintf(`%s Session "SID-ALICE-1": The session has been terminated. The statistical information is as follows: Total outgoing data size: 900 bytes, Total incoming data size: 1800 bytes, Total outgoing unicast packets: 12 packets, Total incoming unicast packets: 8 packets.`, fmtLogTs(tExactEnd)),
		fmt.Sprintf(`%s Connection "CID-1" terminated by the cause "The VPN session has been deleted." (code 11).`, fmtLogTs(tExactEnd)),
		fmt.Sprintf(`%s Connection "CID-2": Connection started from 198.51.100.55 (port 51234).`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Connection "CID-2": Successfully authenticated as user "bob"`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Connection "CID-2": A new session "SID-BOB-2" has been created. (IP address: 198.51.100.55, Port number: 51234, Physical protocol: "OpenVPN")`, fmtLogTs(tBobStart)),
		fmt.Sprintf(`%s Session "SID-BOB-2": The session has been terminated. The statistical information is as follows: Total outgoing data size: 3400 bytes, Total incoming data size: 12000 bytes.`, fmtLogTs(tBobEnd)),
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
	if str(alice["protocol"]) != "SoftEther SSL-VPN" {
		t.Fatalf("expected alice Standard TCP/IP session to resolve as SoftEther SSL-VPN, got %q", str(alice["protocol"]))
	}
}

func TestDetectSessionProtocolAndLogParsingForL2TPIPsec(t *testing.T) {
	cases := []struct {
		name   string
		s      M
		status M
		want   string
	}{
		{"l2tp tag", M{"Name_str": "SID-SEAN-[L2TP]-12"}, nil, "L2TP/IPsec"},
		{"l2tp3 tag", M{"Name_str": "SID-SEAN-[L2TP3]-4"}, nil, "L2TP/IPsec"},
		{"l2tp underlying", nil, M{"UnderlayProtocol_str": "Legacy Protocol - L2TPv2 over IPsec"}, "L2TP/IPsec"},
		{"ppp product", nil, M{"ClientProductName_str": "PPP Client"}, "L2TP/IPsec"},
		{"ipsec port 4500", nil, M{"ServerPort2_u32": 4500}, "L2TP/IPsec"},
		{"l2tp port 1701", M{"ClientPort": 1701}, nil, "L2TP/IPsec"},
		{"ikev2", nil, M{"UnderlayProtocol_str": "IKEv2 / IPsec"}, "IKEv2/IPsec"},
		{"etherip", M{"Name_str": "SID-SEAN-[ETHERIP]-1"}, nil, "EtherIP/IPsec"},
		{"sstp", M{"Name_str": "SID-SEAN-[SSTP]-2"}, nil, "MS-SSTP"},
		{"wireguard bracket", M{"Name_str": "SID-SEAN-[WG]-7"}, nil, "WireGuard"},
		{"openvpn l3", M{"Name_str": "SID-SEAN-[OPENVPN_L3]-3"}, nil, "OpenVPN"},
		{"standard tcpip", nil, M{"UnderlayProtocol_str": "Standard TCP/IP (IPv4)"}, "SoftEther SSL-VPN"},
		{"tls cipher fallback", nil, M{"CipherName_str": "TLS_AES_256_GCM_SHA384"}, "SoftEther SSL-VPN"},
		{"username does not false match", M{"Username_str": "ppptest", "Name_str": "SID-PPPTEST-1"}, nil, ""},
	}
	for _, tc := range cases {
		if got := detectSessionProtocol(tc.s, tc.status); got != tc.want {
			t.Errorf("%s: detectSessionProtocol() = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Real SoftEther security_log wording: "Physical underlying protocol:", "VPN Client details:", and DHCP assignment.
	logText := strings.Join([]string{
		`2026-11-19 10:30:13.000 Connection "CID-10": The IP address of the client is 221.224.210.178, and the port number is 1701.`,
		`2026-11-19 10:30:13.100 Connection "CID-10": User authentication succeeded. User name: "sean".`,
		`2026-11-19 10:30:13.200 Connection "CID-10": The new session "SID-SEAN-[L2TP]-9" has been created. (IP address: 221.224.210.178, Port number: 1701, Physical underlying protocol: "Legacy Protocol - L2TPv2 over IPsec")`,
		`2026-11-19 10:30:14.000 Session "SID-SEAN-[L2TP]-9": The DHCP Server has assigned the IP address 192.168.30.10 to the client (Host name: "Sean-iPhone").`,
		`2026-11-19 11:23:27.000 Session "SID-SEAN-[L2TP]-9": The session has been terminated. Total outgoing data size: 2420000 bytes, Total incoming data size: 6530000 bytes.`,
		`2026-11-19 14:01:15.000 SSL communication for connection "CID-11" has been started.`,
		`2026-11-19 14:01:15.100 Connection "CID-11": The IP address of the client is 221.224.210.178, and the port number is 54321.`,
		`2026-11-19 14:01:15.200 Connection "CID-11": User authentication succeeded. User name: "sean".`,
		`2026-11-19 14:01:15.300 Connection "CID-11": The new session "SID-SEAN-11" has been created. (IP address: 221.224.210.178, Port number: 54321, Physical underlying protocol: "Standard TCP/IP (IPv4)")`,
		`2026-11-19 14:01:15.400 Session "SID-SEAN-11": VPN Client details: (Client product name: "Microsoft SSTP VPN Client", Client version: 100)`,
		`2026-11-19 14:03:17.000 Session "SID-SEAN-11": The session has been terminated. Total outgoing data size: 160000 bytes, Total incoming data size: 774000 bytes.`,
		"",
	}, "\n")

	events := parseSecurityLogEvents("DEFAULT", logText, time.UTC)
	if len(events) != 2 {
		t.Fatalf("expected 2 parsed events, got %d: %+v", len(events), events)
	}
	if events[0].protocol != "L2TP/IPsec" || events[0].virtualIP != "192.168.30.10" || events[0].clientHost != "Sean-iPhone" {
		t.Fatalf("event 0 mismatch: %+v", events[0])
	}
	// Specific MS-SSTP from "VPN Client details" must upgrade generic SoftEther SSL-VPN from SSL/Standard TCP/IP.
	if events[1].protocol != "MS-SSTP" {
		t.Fatalf("event 1 expected MS-SSTP upgrade from VPN Client details, got %q", events[1].protocol)
	}
}

func TestSecurityLogTimezoneCalibrationCanonicalUserAndEndOnlyMerge(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	ctx := context.Background()

	cst := time.FixedZone("CST", 8*3600)
	nowUTC := time.Now().UTC().Truncate(time.Second)
	pollStart := nowUTC.Add(-5 * time.Minute)
	pollEnd := pollStart.Add(2*time.Minute + 12*time.Second)
	logEndUTC := pollEnd.Add(47 * time.Second) // SoftEther terminates WG ~47s after poll saw it end

	// Seed the real polled WireGuard session for lowercase "sean".
	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,VirtualIP,StartedDate,EndedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,EndedTs,Protocol,DisconnectReason)
		VALUES(1,'DEFAULT','sean','wg-real-uid','112.86.92.109','192.168.30.2',?,?,?,198000,68000,130000,?,?,'WireGuard','WireGuard peer inactive timeout')`,
		pollStart.Format(time.RFC3339), pollEnd.Format(time.RFC3339), pollEnd.Format(time.RFC3339), pollStart.Unix(), pollEnd.Unix())

	// SoftEther writes CST (+08:00) wall time into security_log while EnumLogFile.UpdatedTime_dt is UTC.
	// Also test an end-only WireGuard termination line with uppercase SID-SEAN-[WIREGUARD]-1.
	logDay := logEndUTC.In(cst).Format("20060102")
	logPath := fmt.Sprintf("security_log/DEFAULT/sec_%s.log", logDay)
	logBuf := fmt.Sprintf(
		"%s Session \"SID-SEAN-[WIREGUARD]-1\": The session has been terminated. Total outgoing data size: 133000 bytes, Total incoming data size: 68500 bytes.\n",
		logEndUTC.In(cst).Format("2006-01-02 15:04:05.000"),
	)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		switch req["method"] {
		case "GetServerStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"CurrentTime_dt": nowUTC.Format(time.RFC3339)}})
		case "EnumLogFile":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"LogFiles": []M{{
				"FilePath_str":   logPath,
				"ServerName_str": "vpn1",
				"FileSize_u64":   len(logBuf),
				"UpdatedTime_dt": logEndUTC.Format(time.RFC3339),
			}}}})
		case "ReadLogFile":
			p := object(req["params"])
			off := int(integer(p["Offset_u32"]))
			if off > len(logBuf) {
				off = len(logBuf)
			}
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"Buffer_bin": base64.StdEncoding.EncodeToString([]byte(logBuf[off:])),
			}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	a.syncSessionLogs(ctx, 1)

	code, body, _ := call(t, a, "GET", "/api/v1/server/session-history", nil, token)
	if code != 200 {
		t.Fatalf("session-history status=%d body=%v", code, body)
	}
	items := list(body["items"])
	if len(items) != 1 {
		t.Fatalf("expected end-only CST WireGuard log to merge into existing polled session without duplicate/future row, got %d items: %v", len(items), items)
	}
	if str(items[0]["username"]) != "sean" || integer(items[0]["download_bytes"]) != 68500 || integer(items[0]["upload_bytes"]) != 133000 {
		t.Fatalf("expected merged canonical lowercase sean with calibrated down/up bytes, got %v", items[0])
	}
}
