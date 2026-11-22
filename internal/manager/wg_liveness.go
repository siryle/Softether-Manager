package manager

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

type wgSessTrack struct {
	lastActivityAt  time.Time
	lastSeenAt      time.Time
	recvBytes       int64
	recvPackets     int64
	recvReal        int64
	sendUnicast     int64
	sendBroadcast   int64
	lastComm        time.Time
	reapAttemptedAt time.Time
	reaped          bool
}

func (a *App) wireguardIdleTimeout() time.Duration {
	secs := integer(a.setting("wireguard_idle_timeout_seconds"))
	if secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func isWireGuardSession(s, status M) bool {
	return detectSessionProtocol(s, status) == "WireGuard"
}

func wgTrackKey(serverID int64, hub string, s M) string {
	sessName := strings.TrimSpace(str(s["Name_str"]))
	uid := strings.TrimSpace(str(s["UniqueId_bin"]))
	if !validSessionUID(uid) {
		uid = strings.TrimSpace(str(s["CreatedTime_dt"]))
	}
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", serverID, hub, sessName, uid)
}

func parseValidSoftEtherTime(raw string) time.Time {
	t := parseTime(strings.TrimSpace(raw)).UTC()
	if t.IsZero() || t.Year() < 2000 {
		return time.Time{}
	}
	return t
}

func extractWGMetrics(s, status M) (recvBytes, recvPackets, recvReal, sendUnicast, sendBroadcast int64, lastComm, created time.Time) {
	if status != nil {
		recvBytes = integer(status["Recv.UnicastBytes_u64"]) + integer(status["Recv.BroadcastBytes_u64"])
		recvPackets = integer(status["Recv.UnicastCount_u64"]) + integer(status["Recv.BroadcastCount_u64"])
		recvReal = integer(status["TotalRecvSizeReal_u64"])
		sendUnicast = integer(status["Send.UnicastCount_u64"])
		sendBroadcast = integer(status["Send.BroadcastCount_u64"])
		lastComm = parseValidSoftEtherTime(str(status["LastCommTime_dt"]))
		created = parseValidSoftEtherTime(str(status["StartTime_dt"]))
	}
	if s != nil {
		if lastComm.IsZero() {
			lastComm = parseValidSoftEtherTime(str(s["LastCommTime_dt"]))
		}
		if created.IsZero() {
			created = parseValidSoftEtherTime(str(s["CreatedTime_dt"]))
		}
	}
	return
}

// observeWireGuardSession tracks inbound client-to-server activity for a WireGuard session.
// SoftEther 5.x's native WireGuard module (UDP/stateless) can keep disconnected sessions
// listed in EnumSession for minutes or indefinitely (especially when hub broadcasts hit
// the session). Tracking client-originated counters (Recv.* / TotalRecvSizeReal) and
// non-broadcast LastCommTime advances detects dead peers without false positives from
// server-side broadcast traffic.
func (a *App) observeWireGuardSession(serverID int64, hub string, s, status M, at time.Time) (bool, time.Time) {
	timeout := a.wireguardIdleTimeout()
	if timeout <= 0 || !isWireGuardSession(s, status) {
		return false, at
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}

	key := wgTrackKey(serverID, hub, s)
	recvBytes, recvPackets, recvReal, sendUnicast, sendBroadcast, lastComm, created := extractWGMetrics(s, status)
	hasStatus := status != nil

	a.wgTrackMu.Lock()
	defer a.wgTrackMu.Unlock()
	if a.wgTrack == nil {
		a.wgTrack = make(map[string]*wgSessTrack)
	}

	prev, exists := a.wgTrack[key]
	if !exists {
		lastAct := at
		// On first sight (e.g. right after restart), trust SoftEther's LastCommTime_dt as
		// already stale only when clock skew is bounded and either SoftEther demonstrably
		// advanced LastCommTime past session creation, or the peer never sent any bytes.
		clockOK := created.IsZero() || !created.After(at.Add(2*time.Minute))
		if clockOK && !lastComm.IsZero() && lastComm.Before(at) {
			if (!created.IsZero() && lastComm.After(created.Add(5*time.Second))) || (hasStatus && recvBytes == 0 && recvPackets == 0 && recvReal == 0) {
				lastAct = lastComm
			}
		}
		prev = &wgSessTrack{
			lastActivityAt: lastAct,
			lastSeenAt:     at,
			recvBytes:      recvBytes,
			recvPackets:    recvPackets,
			recvReal:       recvReal,
			sendUnicast:    sendUnicast,
			sendBroadcast:  sendBroadcast,
			lastComm:       lastComm,
		}
		a.wgTrack[key] = prev
	} else {
		inboundChanged := hasStatus && (recvBytes > prev.recvBytes || recvPackets > prev.recvPackets || recvReal > prev.recvReal)
		// If SoftEther updates LastCommTime on WireGuard keepalive/handshake packets while
		// outbound hub counters did not move, count that as genuine client activity too.
		keepaliveChanged := !lastComm.IsZero() && !prev.lastComm.IsZero() && lastComm.After(prev.lastComm) &&
			(!hasStatus || (sendBroadcast == prev.sendBroadcast && sendUnicast == prev.sendUnicast))

		if inboundChanged || keepaliveChanged {
			prev.lastActivityAt = at
			prev.reaped = false
		}
		if hasStatus {
			if recvBytes > prev.recvBytes {
				prev.recvBytes = recvBytes
			}
			if recvPackets > prev.recvPackets {
				prev.recvPackets = recvPackets
			}
			if recvReal > prev.recvReal {
				prev.recvReal = recvReal
			}
			prev.sendUnicast = sendUnicast
			prev.sendBroadcast = sendBroadcast
		}
		if !lastComm.IsZero() && (prev.lastComm.IsZero() || lastComm.After(prev.lastComm)) {
			prev.lastComm = lastComm
		}
		prev.lastSeenAt = at
	}

	if at.Sub(prev.lastActivityAt) >= timeout {
		prev.reaped = true
		return true, prev.lastActivityAt
	}
	return false, prev.lastActivityAt
}

func (a *App) isWireGuardReaped(serverID int64, hub string, s M) bool {
	if a.wireguardIdleTimeout() <= 0 || !isWireGuardSession(s, nil) {
		return false
	}
	key := wgTrackKey(serverID, hub, s)
	a.wgTrackMu.Lock()
	defer a.wgTrackMu.Unlock()
	if prev, ok := a.wgTrack[key]; ok {
		return prev.reaped
	}
	return false
}

func (a *App) shouldAttemptWGReap(serverID int64, hub string, s M, at time.Time) bool {
	key := wgTrackKey(serverID, hub, s)
	a.wgTrackMu.Lock()
	defer a.wgTrackMu.Unlock()
	if a.wgTrack == nil {
		a.wgTrack = make(map[string]*wgSessTrack)
	}
	prev, ok := a.wgTrack[key]
	if !ok {
		return true
	}
	if !prev.reapAttemptedAt.IsZero() && at.Sub(prev.reapAttemptedAt) < 15*time.Second {
		return false
	}
	prev.reapAttemptedAt = at
	return true
}

func (a *App) clearWGTrackByName(serverID int64, hub, sessName string) {
	prefix := fmt.Sprintf("%d\x00%s\x00%s\x00", serverID, hub, strings.TrimSpace(sessName))
	a.wgTrackMu.Lock()
	defer a.wgTrackMu.Unlock()
	for k := range a.wgTrack {
		if strings.HasPrefix(k, prefix) {
			delete(a.wgTrack, k)
		}
	}
}

func (a *App) pruneWGTracker(at time.Time) {
	a.wgTrackMu.Lock()
	defer a.wgTrackMu.Unlock()
	for k, v := range a.wgTrack {
		if at.Sub(v.lastSeenAt) > 10*time.Minute {
			delete(a.wgTrack, k)
		}
	}
}

// closeSessionSample closes any open VpnSessionSample matching the disconnected session
// immediately so Audit Report and User Detail reflect the disconnect without waiting for
// the next session sampling tick.
func (a *App) closeSessionSample(serverID int64, hub, sessName, sessKey, username, clientIP string, startedAt, endedAt time.Time, reason string) {
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	} else {
		endedAt = endedAt.UTC()
	}
	endedISO := endedAt.Format(time.RFC3339Nano)
	endedTs := endedAt.Unix()
	var startedTs int64
	if !startedAt.IsZero() && startedAt.Year() >= 2000 {
		startedTs = startedAt.UTC().Unix()
	}
	_, err := a.db.Exec(
		`UPDATE VpnSessionSample
		 SET EndedDate = CASE WHEN julianday(?) < julianday(StartedDate) THEN StartedDate ELSE ? END,
		     EndedTs = CASE WHEN ? < COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) THEN COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) ELSE ? END,
		     LastSeenDate = CASE WHEN julianday(?) < julianday(StartedDate) THEN StartedDate ELSE ? END,
		     DisconnectReason = CASE WHEN ? != '' AND DisconnectReason = '' THEN ? ELSE DisconnectReason END
		 WHERE ServerID = ? AND HubName = ? AND EndedDate IS NULL
		   AND (
		     (? != '' AND SessionKey = ?)
		     OR (? != '' AND (SessionKey LIKE 'sess:' || ? || '@%' OR SessionKey LIKE '%#' || ? || '@%'))
		     OR (? > 0 AND ? != '' AND UserName = ? COLLATE NOCASE AND (? = '' OR ClientIP = ?) AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 30)
		   )`,
		endedISO, endedISO, endedTs, endedTs, endedISO, endedISO,
		reason, reason,
		serverID, hub,
		sessKey, sessKey,
		sessName, sessName, sessName,
		startedTs, username, username, clientIP, clientIP, startedTs,
	)
	if err != nil {
		log.Printf("close session sample %d/%s/%s: %v", serverID, hub, sessName, err)
	}
}

func (a *App) markSessionClosedNow(serverID int64, hub, sessName, reason string) {
	a.clearWGTrackByName(serverID, hub, sessName)
	a.closeSessionSample(serverID, hub, sessName, "", "", "", time.Time{}, time.Now().UTC(), reason)
}

func (a *App) reapWireGuardSession(ctx context.Context, serverID int64, hub string, s M, lastActivity time.Time) {
	sessName := strings.TrimSpace(str(s["Name_str"]))
	if sessName == "" {
		return
	}
	nowUTC := time.Now().UTC()
	if !a.shouldAttemptWGReap(serverID, hub, s, nowUTC) {
		return
	}

	sessKey := strings.TrimSpace(str(s["UniqueId_bin"]))
	if !validSessionUID(sessKey) {
		sessKey = ""
	}
	username := strings.TrimSpace(str(s["Username_str"]))
	clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
	startedAt := parseValidSoftEtherTime(str(s["CreatedTime_dt"]))

	a.closeSessionSample(serverID, hub, sessName, sessKey, username, clientIP, startedAt, lastActivity, "WireGuard peer inactive timeout")

	callCtx, cancel := context.WithTimeout(context.WithValue(context.WithoutCancel(ctx), serverIDKey, serverID), 8*time.Second)
	defer cancel()
	if _, err := a.client(callCtx).Call(callCtx, "DeleteSession", M{"HubName_str": hub, "Name_str": sessName}); err != nil {
		log.Printf("wireguard reap %d/%s/%s: %v", serverID, hub, sessName, err)
	}
}

// wgLivenessTick periodically checks active WireGuard sessions across online servers and
// tears down stale sessions whose peer has stopped sending packets.
func (a *App) wgLivenessTick(ctx context.Context) {
	if a.wireguardIdleTimeout() <= 0 {
		return
	}
	nowUTC := time.Now().UTC()
	a.pruneWGTracker(nowUTC)

	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	if len(servers) == 0 {
		return
	}
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, row := range servers {
		serverID := integer(row["ServerID"])
		wg.Add(1)
		go func(sid int64) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("wireguard liveness server %d: %v", sid, p)
				}
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			sCtx, cancel := context.WithTimeout(context.WithValue(ctx, serverIDKey, sid), 12*time.Second)
			defer cancel()
			hubs := list(a.rpc(sCtx, "EnumHub", nil)["HubList"])
			for _, h := range hubs {
				if !boolean(h["Online_bool"]) {
					continue
				}
				hub := str(h["HubName_str"])
				if hub == "" {
					continue
				}
				sessions := list(a.rpc(sCtx, "EnumSession", M{"HubName_str": hub})["SessionList"])
				for _, s := range sessions {
					if !isClientSessionRPC(s) || !isWireGuardSession(s, nil) {
						continue
					}
					sessName := strings.TrimSpace(str(s["Name_str"]))
					if sessName == "" {
						continue
					}
					var st M
					func() {
						defer func() { _ = recover() }()
						st = a.rpc(sCtx, "GetSessionStatus", M{"HubName_str": hub, "Name_str": sessName})
					}()
					if stale, lastAct := a.observeWireGuardSession(sid, hub, s, st, nowUTC); stale {
						a.reapWireGuardSession(sCtx, sid, hub, s, lastAct)
					}
				}
			}
		}(serverID)
	}
	wg.Wait()
}
