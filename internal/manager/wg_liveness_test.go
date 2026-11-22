package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWireGuardStaleSessionDetectionAndReap(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	a.set("wireguard_idle_timeout_seconds", 60)

	t0 := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	wgSess := M{
		"Name_str":       "SID-SEAN-[WIREGUARD]-1",
		"Username_str":   "sean",
		"UniqueId_bin":   "wg-uid-1",
		"ClientIP_ip":    "10.10.10.254",
		"CreatedTime_dt": t0.Format(time.RFC3339),
	}

	// 1. Active client: inbound Recv counters increase -> never stale.
	st1 := M{
		"UnderlayProtocol_str":     "WireGuard",
		"Recv.UnicastBytes_u64":    1000,
		"Recv.UnicastCount_u64":    10,
		"Send.BroadcastCount_u64":  5,
		"LastCommTime_dt":          t0.Add(4 * time.Minute).Format(time.RFC3339),
	}
	if stale, _ := a.observeWireGuardSession(1, "DEFAULT", wgSess, st1, t0.Add(4*time.Minute)); stale {
		t.Fatalf("fresh active WG session must not be stale")
	}
	st2 := M{
		"UnderlayProtocol_str":     "WireGuard",
		"Recv.UnicastBytes_u64":    2000,
		"Recv.UnicastCount_u64":    20,
		"Send.BroadcastCount_u64":  8,
		"LastCommTime_dt":          t0.Add(4*time.Minute + 40*time.Second).Format(time.RFC3339),
	}
	if stale, _ := a.observeWireGuardSession(1, "DEFAULT", wgSess, st2, t0.Add(4*time.Minute+40*time.Second)); stale {
		t.Fatalf("WG session with inbound growth must not be stale")
	}

	// 2. Keepalive-only update: LastCommTime advances while Send counters don't move -> still alive.
	stKeepalive := M{
		"UnderlayProtocol_str":     "WireGuard",
		"Recv.UnicastBytes_u64":    2000,
		"Recv.UnicastCount_u64":    20,
		"Send.BroadcastCount_u64":  8,
		"LastCommTime_dt":          t0.Add(5*time.Minute + 20*time.Second).Format(time.RFC3339),
	}
	if stale, _ := a.observeWireGuardSession(1, "DEFAULT", wgSess, stKeepalive, t0.Add(5*time.Minute+20*time.Second)); stale {
		t.Fatalf("WG session with non-broadcast LastCommTime keepalive must not be stale")
	}

	// 3. Disconnected client: hub broadcasts keep ticking (Send.BroadcastCount grows + LastCommTime moves),
	//    but all Recv/TotalRecvSizeReal stay frozen for >= 60s -> MUST be detected stale!
	stDead1 := M{
		"UnderlayProtocol_str":     "WireGuard",
		"Recv.UnicastBytes_u64":    2000,
		"Recv.UnicastCount_u64":    20,
		"Send.BroadcastCount_u64":  15,
		"LastCommTime_dt":          t0.Add(6*time.Minute + 25*time.Second).Format(time.RFC3339),
	}
	stale, lastAct := a.observeWireGuardSession(1, "DEFAULT", wgSess, stDead1, t0.Add(6*time.Minute+25*time.Second))
	if !stale {
		t.Fatalf("disconnected WG session with only broadcast traffic for >60s must be detected stale")
	}
	if !lastAct.Equal(t0.Add(5*time.Minute + 20*time.Second)) {
		t.Fatalf("expected lastActivity=%v, got %v", t0.Add(5*time.Minute+20*time.Second), lastAct)
	}

	// 4. Non-WireGuard session (SoftEther SSL-VPN) must NEVER be marked stale.
	sslSess := M{"Name_str": "SID-ALICE-2", "Username_str": "alice", "UniqueId_bin": "ssl-uid-1", "CreatedTime_dt": t0.Format(time.RFC3339)}
	sslSt := M{"UnderlayProtocol_str": "Standard TCP/IP (IPv4)", "Recv.UnicastBytes_u64": 0}
	if stale, _ := a.observeWireGuardSession(1, "DEFAULT", sslSess, sslSt, t0.Add(30*time.Minute)); stale {
		t.Fatalf("non-WG session must never be marked stale by WG tracker")
	}

	// 5. End-to-end: disconnected WG session is reaped via DeleteSession, omitted from /clients & /users, and closed in VpnSessionSample.
	var mu sync.Mutex
	deleted := []string{}
	oldStart := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	oldLastComm := oldStart.Add(2 * time.Minute) // Updated after start, now 8m stale (> 60s)!

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		mu.Lock()
		defer mu.Unlock()
		switch req["method"] {
		case "EnumHub":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"HubList": []M{{"HubName_str": "DEFAULT", "Online_bool": true}}}})
		case "EnumUser":
			writeJSON(w, 200, M{"id": req["id"], "result": M{"UserList": []M{{"Name_str": "sean"}}}})
		case "EnumSession":
			if len(deleted) > 0 {
				writeJSON(w, 200, M{"id": req["id"], "result": M{"SessionList": []M{}}})
				return
			}
			writeJSON(w, 200, M{"id": req["id"], "result": M{"SessionList": []M{{
				"Name_str":        "SID-SEAN-[WIREGUARD]-9",
				"Username_str":    "sean",
				"UniqueId_bin":    "wg-ghost-uid",
				"ClientIP_ip":     "10.10.10.254",
				"CreatedTime_dt":  oldStart.Format(time.RFC3339),
				"LastCommTime_dt": oldLastComm.Format(time.RFC3339),
				"PacketSize_u64":  4096,
			}}}})
		case "GetSessionStatus":
			writeJSON(w, 200, M{"id": req["id"], "result": M{
				"UnderlayProtocol_str":  "WireGuard",
				"StartTime_dt":          oldStart.Format(time.RFC3339),
				"LastCommTime_dt":       oldLastComm.Format(time.RFC3339),
				"Recv.UnicastBytes_u64": 1500,
				"Recv.UnicastCount_u64": 12,
				"Send.UnicastBytes_u64": 2596,
			}})
		case "DeleteSession":
			p := object(req["params"])
			deleted = append(deleted, str(p["Name_str"]))
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		default:
			writeJSON(w, 200, M{"id": req["id"], "result": M{}})
		}
	}))
	defer srv.Close()
	connectAuditServer(t, a, srv, 1)

	execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,StartedDate,LastSeenDate,BytesTotal,DownloadBytes,UploadBytes,StartedTs,Protocol) VALUES(1,'DEFAULT','sean','wg-ghost-uid','10.10.10.254',?,?,4096,2596,1500,?,'WireGuard')`,
		oldStart.Format(time.RFC3339), now(), oldStart.Unix())

	// GET /clients must exclude the stale ghost WG session and trigger DeleteSession.
	code, body, _ := call(t, a, "GET", "/api/v1/clients", nil, token)
	if code != 200 {
		t.Fatalf("GET /clients status=%d body=%v", code, body)
	}
	if len(list(body["clients"])) != 0 {
		t.Fatalf("expected stale WG ghost session to be excluded from /clients, got %v", body["clients"])
	}

	// Wait briefly for the async reap goroutine to finish DeleteSession.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(deleted)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	mu.Lock()
	gotDeleted := append([]string(nil), deleted...)
	mu.Unlock()
	if len(gotDeleted) != 1 || gotDeleted[0] != "SID-SEAN-[WIREGUARD]-9" {
		t.Fatalf("expected DeleteSession to reap ghost WG session, got %v", gotDeleted)
	}

	// VpnSessionSample must be closed immediately with lastActivity timestamp & reason.
	row := one(a.db, `SELECT EndedDate, DisconnectReason FROM VpnSessionSample WHERE SessionKey='wg-ghost-uid'`)
	if row == nil || str(row["EndedDate"]) == "" || str(row["DisconnectReason"]) != "WireGuard peer inactive timeout" {
		t.Fatalf("expected ghost WG session sample to be closed cleanly, got %v", row)
	}

	// GET /users must report sean as offline.
	code, uBody, _ := call(t, a, "GET", "/api/v1/users", nil, token)
	if code != 200 {
		t.Fatalf("GET /users status=%d", code)
	}
	users := list(uBody["users"])
	if len(users) != 1 || boolean(users[0]["Online_bool"]) {
		t.Fatalf("expected sean to be offline in /users after WG disconnect, got %v", users)
	}

	// Also verify wgLivenessTick runs cleanly without error.
	a.wgLivenessTick(context.Background())
}
