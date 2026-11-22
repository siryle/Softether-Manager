package manager

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	reLogTimestamp     = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})(?:\.\d+)?\s+(.*)$`)
	reLogConnStart     = regexp.MustCompile(`Connection "([^"]+)".*?(?:IP address[^0-9a-fA-F:]*|from\s+)([0-9a-fA-F:.]+).*?(?:port(?:\s+number)?(?:\s+is)?[:\s]+)(\d+)`)
	reLogSSLStart      = regexp.MustCompile(`SSL communication for connection "([^"]+)"`)
	reLogConnAuth      = regexp.MustCompile(`Connection "([^"]+)".*?(?:User authentication succeeded\.?\s*User name:\s*"([^"]+)"|Successfully authenticated as user "([^"]+)"|Authenticated as "([^"]+)")`)
	reLogSessCreate    = regexp.MustCompile(`(?i)Connection "([^"]+)".*?new session "([^"]+)" has been created`)
	reLogSessCreateAlt = regexp.MustCompile(`(?i)Session "([^"]+)".*?has been created`)
	reLogSessStart     = regexp.MustCompile(`Session "([^"]+)".*?(?:has been (?:started|established)|logged in)`)
	reLogSessDetails   = regexp.MustCompile(`Session "([^"]+)":.*?(?:VPN Client details|Client product name|Physical(?:\s+underlying)?\s+protocol)`)
	reLogSessDHCP      = regexp.MustCompile(`Session "([^"]+)":.*DHCP.*?(?:IP address|assigned)[:\s]+([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
	reLogDHCPHost      = regexp.MustCompile(`Host name:\s*"([^"]+)"`)
	reLogSessEnd       = regexp.MustCompile(`Session "([^"]+)": The session has been terminated\.(.*)`)
	reLogConnTerm      = regexp.MustCompile(`Connection "([^"]+)" terminated by the cause "([^"]+)"(?:\s*\(code\s*(\d+)\))?`)
	reLogConnRef       = regexp.MustCompile(`Connection "([^"]+)"`)
	reLogOutBytes      = regexp.MustCompile(`Total outgoing data size:\s*(\d+)\s*bytes`)
	reLogInBytes       = regexp.MustCompile(`Total incoming data size:\s*(\d+)\s*bytes`)
	reLogPackets       = regexp.MustCompile(`Total (?:outgoing|incoming) (?:unicast|broadcast) packets:\s*(\d+)\s*packets`)
	reLogClientIP      = regexp.MustCompile(`(?:IP address(?:\s+of the client)?(?:\s+is)?[:\s]+)([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|[0-9a-fA-F:]+:[0-9a-fA-F:]+)`)
	reLogPort          = regexp.MustCompile(`[Pp]ort(?:\s+number)?(?:\s+is)?[:\s]+(\d+)`)
	reLogProto         = regexp.MustCompile(`Physical(?:\s+underlying)?\s+protocol:\s*"?([^",)]+)"?`)
	reLogProduct       = regexp.MustCompile(`Client product name:\s*"([^"]+)"`)
	reLogSecFile       = regexp.MustCompile(`(?i)(?:^|/)security_log/([^/]+)/sec_(\d{8})\.log$`)
)

type logConnMeta struct {
	user       string
	clientIP   string
	clientPort int64
	protocol   string
	cause      string
	seenAt     time.Time
}

type parsedLogSession struct {
	hub              string
	sessionName      string
	username         string
	clientIP         string
	virtualIP        string
	clientHost       string
	clientPort       int64
	protocol         string
	hasStart         bool
	startedAt        time.Time
	endedAt          time.Time
	downBytes        int64
	upBytes          int64
	packetsTotal     int64
	disconnectReason string
}

func parseLogLineTime(raw string, loc *time.Location) (time.Time, string, bool) {
	m := reLogTimestamp.FindStringSubmatch(strings.TrimSpace(raw))
	if len(m) != 3 {
		return time.Time{}, "", false
	}
	if loc == nil {
		loc = time.Local
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], loc)
	if err != nil {
		return time.Time{}, "", false
	}
	return t.UTC(), m[2], true
}

func inferUsernameFromSessionName(sessName string) string {
	// SoftEther names sessions SID-<USERNAME>-<NUMBER> (or SID-<USERNAME>-[L2TP]-<N>, etc.).
	upper := strings.ToUpper(sessName)
	if !strings.HasPrefix(upper, "SID-") {
		return ""
	}
	rest := sessName[4:]
	if idx := strings.LastIndex(rest, "-"); idx > 0 {
		candidate := rest[:idx]
		// Strip trailing protocol tags like -[L2TP] or -[OPENVPN] if present.
		if strings.HasSuffix(candidate, "]") {
			if bIdx := strings.LastIndex(candidate, "-["); bIdx > 0 {
				candidate = candidate[:bIdx]
			}
		}
		if candidate != "" && !strings.HasPrefix(strings.ToLower(candidate), "securenat") && !strings.HasPrefix(strings.ToLower(candidate), "local-bridge") {
			return candidate
		}
	}
	return ""
}

func parseSecurityLogEvents(hub, text string, loc *time.Location) []parsedLogSession {
	conns := map[string]*logConnMeta{}
	sessToConn := map[string]string{}
	openSessions := map[string]*parsedLogSession{}
	var completed []parsedLogSession

	ensureConn := func(cid string, at time.Time) *logConnMeta {
		c := conns[cid]
		if c == nil {
			c = &logConnMeta{}
			conns[cid] = c
		}
		c.seenAt = at
		return c
	}

	for _, line := range strings.Split(text, "\n") {
		at, body, ok := parseLogLineTime(line, loc)
		if !ok {
			continue
		}
		if m := reLogConnStart.FindStringSubmatch(body); len(m) == 4 {
			c := ensureConn(m[1], at)
			if isIP(m[2]) {
				c.clientIP = cleanIP(m[2])
			}
			if p, err := strconv.ParseInt(m[3], 10, 64); err == nil && p > 0 && p <= 65535 {
				c.clientPort = p
			}
			protoSrc := M{"ClientPort": c.clientPort}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = pm[1]
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = prm[1]
			}
			if p := detectSessionProtocol(protoSrc, nil); p != "" {
				c.protocol = preferProtocol(c.protocol, p)
			}
			continue
		}
		if m := reLogSSLStart.FindStringSubmatch(body); len(m) == 2 {
			c := ensureConn(m[1], at)
			c.protocol = preferProtocol(c.protocol, "SoftEther SSL-VPN")
			continue
		}
		if m := reLogConnAuth.FindStringSubmatch(body); len(m) == 5 {
			c := ensureConn(m[1], at)
			for i := 2; i <= 4; i++ {
				if m[i] != "" {
					c.user = m[i]
					break
				}
			}
			if ipm := reLogClientIP.FindStringSubmatch(body); len(ipm) == 2 && isIP(ipm[1]) && c.clientIP == "" {
				c.clientIP = cleanIP(ipm[1])
			}
			continue
		}
		if m := reLogSessCreate.FindStringSubmatch(body); len(m) == 3 {
			cid, sn := m[1], m[2]
			c := ensureConn(cid, at)
			sessToConn[sn] = cid
			if ipm := reLogClientIP.FindStringSubmatch(body); len(ipm) == 2 && isIP(ipm[1]) {
				c.clientIP = cleanIP(ipm[1])
			}
			if pm := reLogPort.FindStringSubmatch(body); len(pm) == 2 {
				if p, err := strconv.ParseInt(pm[1], 10, 64); err == nil && p > 0 && p <= 65535 {
					c.clientPort = p
				}
			}
			protoSrc := M{"Name_str": sn, "ClientPort": c.clientPort}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = strings.TrimSpace(pm[1])
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = strings.TrimSpace(prm[1])
			}
			if p := detectSessionProtocol(protoSrc, nil); p != "" {
				c.protocol = preferProtocol(c.protocol, p)
			}
			user := c.user
			if user == "" {
				user = inferUsernameFromSessionName(sn)
			}
			openSessions[sn] = &parsedLogSession{
				hub:         hub,
				sessionName: sn,
				username:    user,
				clientIP:    c.clientIP,
				clientPort:  c.clientPort,
				protocol:    c.protocol,
				hasStart:    true,
				startedAt:   at,
			}
			continue
		}
		if m := reLogSessCreateAlt.FindStringSubmatch(body); len(m) == 2 {
			sn := m[1]
			var cip string
			var cport int64
			if ipm := reLogClientIP.FindStringSubmatch(body); len(ipm) == 2 && isIP(ipm[1]) {
				cip = cleanIP(ipm[1])
			}
			if pm := reLogPort.FindStringSubmatch(body); len(pm) == 2 {
				if p, err := strconv.ParseInt(pm[1], 10, 64); err == nil && p > 0 && p <= 65535 {
					cport = p
				}
			}
			protoSrc := M{"Name_str": sn, "ClientPort": cport}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = strings.TrimSpace(pm[1])
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = strings.TrimSpace(prm[1])
			}
			proto := detectSessionProtocol(protoSrc, nil)
			openSessions[sn] = &parsedLogSession{
				hub:         hub,
				sessionName: sn,
				username:    inferUsernameFromSessionName(sn),
				clientIP:    cip,
				clientPort:  cport,
				protocol:    proto,
				hasStart:    true,
				startedAt:   at,
			}
			continue
		}
		if m := reLogSessDetails.FindStringSubmatch(body); len(m) == 2 {
			sn := m[1]
			protoSrc := M{"Name_str": sn}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = strings.TrimSpace(pm[1])
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = strings.TrimSpace(prm[1])
			}
			if p := detectSessionProtocol(protoSrc, nil); p != "" {
				if sess := openSessions[sn]; sess != nil {
					sess.protocol = preferProtocol(sess.protocol, p)
				}
				if cid := sessToConn[sn]; cid != "" && conns[cid] != nil {
					conns[cid].protocol = preferProtocol(conns[cid].protocol, p)
				}
			}
			continue
		}
		if m := reLogSessDHCP.FindStringSubmatch(body); len(m) == 3 {
			sn, vip := m[1], m[2]
			if sess := openSessions[sn]; sess != nil {
				if sess.virtualIP == "" {
					sess.virtualIP = vip
				}
				if sess.clientHost == "" {
					if hm := reLogDHCPHost.FindStringSubmatch(body); len(hm) == 2 {
						h := strings.TrimSpace(hm[1])
						if h != "" && !isIP(h) {
							sess.clientHost = h
						}
					}
				}
			}
			continue
		}
		if m := reLogSessStart.FindStringSubmatch(body); len(m) == 2 {
			sn := m[1]
			if openSessions[sn] == nil {
				user := inferUsernameFromSessionName(sn)
				var cip string
				var cport int64
				var proto string
				if cid := sessToConn[sn]; cid != "" && conns[cid] != nil {
					if conns[cid].user != "" {
						user = conns[cid].user
					}
					cip = conns[cid].clientIP
					cport = conns[cid].clientPort
					proto = conns[cid].protocol
				}
				if ipm := reLogClientIP.FindStringSubmatch(body); len(ipm) == 2 && isIP(ipm[1]) {
					cip = cleanIP(ipm[1])
				}
				if pm := reLogPort.FindStringSubmatch(body); len(pm) == 2 {
					if p, err := strconv.ParseInt(pm[1], 10, 64); err == nil && p > 0 && p <= 65535 {
						cport = p
					}
				}
				if p := detectSessionProtocol(M{"Name_str": sn, "ClientPort": cport}, nil); p != "" {
					proto = preferProtocol(proto, p)
				}
				openSessions[sn] = &parsedLogSession{
					hub:         hub,
					sessionName: sn,
					username:    user,
					clientIP:    cip,
					clientPort:  cport,
					protocol:    proto,
					hasStart:    true,
					startedAt:   at,
				}
			}
			continue
		}
		if m := reLogConnTerm.FindStringSubmatch(body); len(m) >= 3 {
			cid, cause := m[1], strings.TrimSpace(m[2])
			if len(m) >= 4 && m[3] != "" {
				cause = fmt.Sprintf("%s (code %s)", cause, m[3])
			}
			if c := conns[cid]; c != nil {
				c.cause = cause
			}
			// If a completed session with the same connection just ended right before this line, attach cause.
			for i := len(completed) - 1; i >= 0 && i >= len(completed)-8; i-- {
				if sessToConn[completed[i].sessionName] == cid && completed[i].disconnectReason == "" && at.Sub(completed[i].endedAt) <= 5*time.Second {
					completed[i].disconnectReason = cause
					break
				}
			}
			continue
		}
		if m := reLogSessEnd.FindStringSubmatch(body); len(m) == 3 {
			sn, stats := m[1], m[2]
			var outBytes, inBytes, pkts int64
			if om := reLogOutBytes.FindStringSubmatch(stats); len(om) == 2 {
				outBytes, _ = strconv.ParseInt(om[1], 10, 64)
			}
			if im := reLogInBytes.FindStringSubmatch(stats); len(im) == 2 {
				inBytes, _ = strconv.ParseInt(im[1], 10, 64)
			}
			for _, pm := range reLogPackets.FindAllStringSubmatch(stats, -1) {
				if len(pm) == 2 {
					n, _ := strconv.ParseInt(pm[1], 10, 64)
					pkts += n
				}
			}

			sess := openSessions[sn]
			delete(openSessions, sn)
			if sess == nil {
				user := inferUsernameFromSessionName(sn)
				var cip, proto, cause string
				var cport int64
				if cid := sessToConn[sn]; cid != "" && conns[cid] != nil {
					if conns[cid].user != "" {
						user = conns[cid].user
					}
					cip = conns[cid].clientIP
					cport = conns[cid].clientPort
					proto = conns[cid].protocol
					cause = conns[cid].cause
				}
				sess = &parsedLogSession{
					hub:              hub,
					sessionName:      sn,
					username:         user,
					clientIP:         cip,
					clientPort:       cport,
					protocol:         proto,
					startedAt:        at,
					disconnectReason: cause,
				}
			}
			if sess.username == "" {
				sess.username = inferUsernameFromSessionName(sn)
			}
			if p := detectSessionProtocol(M{"Name_str": sn, "ClientPort": sess.clientPort}, nil); p != "" {
				sess.protocol = preferProtocol(sess.protocol, p)
			}
			if !isClientSessionRPC(M{"Username_str": sess.username, "Name_str": sn}) || sess.username == "" {
				continue
			}
			sess.endedAt = at
			if sess.startedAt.IsZero() || sess.endedAt.Before(sess.startedAt) {
				sess.startedAt = sess.endedAt
			}
			// In SoftEther security_log, "Total outgoing data size" is client->server (upload)
			// and "Total incoming data size" is server->client (download).
			sess.downBytes = inBytes
			sess.upBytes = outBytes
			sess.packetsTotal = pkts
			if sess.disconnectReason == "" {
				if cid := sessToConn[sn]; cid != "" && conns[cid] != nil && conns[cid].cause != "" {
					sess.disconnectReason = conns[cid].cause
				}
			}
			completed = append(completed, *sess)
			continue
		}
		// Catch any other Connection "CID-..." line carrying protocol/product info (e.g., L2TP/IPsec, SSTP, OpenVPN).
		if cm := reLogConnRef.FindStringSubmatch(body); len(cm) == 2 {
			protoSrc := M{}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = strings.TrimSpace(pm[1])
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = strings.TrimSpace(prm[1])
			}
			if len(protoSrc) > 0 {
				if p := detectSessionProtocol(protoSrc, nil); p != "" {
					c := ensureConn(cm[1], at)
					c.protocol = preferProtocol(c.protocol, p)
				}
			}
		}
	}
	return completed
}

func lastLogWallTimeUTC(text string) time.Time {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := reLogTimestamp.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if len(m) == 3 {
			if t, err := time.Parse("2006-01-02 15:04:05", m[1]); err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

// inferLogLocation determines the timezone SoftEther used when writing local
// timestamps into security_log files. SoftEther's RPC timestamps (UpdatedTime_dt,
// CurrentTime_dt) are UTC, while security_log files use the SoftEther host's local
// timezone. Comparing the final log line against EnumLogFile.UpdatedTime_dt
// calibrates the exact offset even when the panel runs in a UTC Docker container.
func inferLogLocation(text string, fileUpdatedUTC, nowUTC time.Time, atEOF bool, fallback *time.Location) *time.Location {
	lastWall := lastLogWallTimeUTC(text)
	if atEOF && !lastWall.IsZero() && !fileUpdatedUTC.IsZero() {
		diff := lastWall.Sub(fileUpdatedUTC)
		roundedSec := int(math.Round(diff.Seconds()/900.0)) * 900
		if math.Abs(diff.Seconds()-float64(roundedSec)) <= 300 && roundedSec >= -12*3600 && roundedSec <= 14*3600 {
			return time.FixedZone("SE", roundedSec)
		}
	}
	if fallback != nil {
		if lastWall.IsZero() {
			return fallback
		}
		_, off := time.Now().In(fallback).Zone()
		if !lastWall.Add(-time.Duration(off) * time.Second).After(nowUTC.Add(5 * time.Minute)) {
			return fallback
		}
	}
	if !lastWall.IsZero() && lastWall.After(nowUTC.Add(2*time.Minute)) {
		// Parsing as UTC puts the log in the future -> the SoftEther host is ahead of UTC.
		_, localOff := nowUTC.In(time.Local).Zone()
		if localOff > 0 && !lastWall.Add(-time.Duration(localOff)*time.Second).After(nowUTC.Add(2*time.Minute)) {
			return time.Local
		}
		hoursAhead := int(math.Ceil(lastWall.Sub(nowUTC).Hours()))
		if hoursAhead >= 1 && hoursAhead <= 14 {
			return time.FixedZone("SE", hoursAhead*3600)
		}
	}
	return time.UTC
}

func (a *App) syncSessionLogs(ctx context.Context, serverID int64) {
	if !boolean(a.setting("session_log_sync_enabled")) {
		return
	}
	var logList []M
	var calibratedLoc *time.Location
	serverNowUTC := time.Now().UTC()
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumLogFile", nil)
		logList = list(out["LogFiles"])
		if len(logList) == 0 {
			logList = list(out["LogFileList"])
		}
	}()
	if len(logList) == 0 {
		return
	}
	func() {
		defer func() { _ = recover() }()
		st := a.rpc(ctx, "GetServerStatus", nil)
		if t := parseTime(str(st["CurrentTime_dt"])); !t.IsZero() {
			serverNowUTC = t.UTC()
			if _, off := t.Zone(); off != 0 {
				calibratedLoc = t.Location()
			}
		}
	}()

	retentionDays := max(int64(1), integer(a.setting("session_history_retention_days")))
	cutoffDay := serverNowUTC.AddDate(0, 0, -int(retentionDays+1)).Format("20060102")

	type logFileTarget struct {
		path       string
		serverName string
		hub        string
		size       int64
		updatedAt  time.Time
	}
	var targets []logFileTarget
	for _, entry := range logList {
		path := str(entry["FilePath_str"])
		m := reLogSecFile.FindStringSubmatch(path)
		if len(m) != 3 {
			continue
		}
		if m[2] < cutoffDay {
			continue
		}
		size := integer(entry["FileSize_u64"])
		if size <= 0 {
			size = integer(entry["FileSize_u32"])
		}
		updatedAt := parseValidSoftEtherTime(str(entry["UpdatedTime_dt"]))
		targets = append(targets, logFileTarget{
			path:       path,
			serverName: str(entry["ServerName_str"]),
			hub:        m[1],
			size:       size,
			updatedAt:  updatedAt,
		})
	}
	if len(targets) == 0 {
		return
	}
	// Process newest files first so timezone calibration from UpdatedTime_dt is immediately
	// available for older files as well.
	sort.Slice(targets, func(i, j int) bool { return targets[i].path > targets[j].path })

	// Load existing cursors in one query.
	cursors := map[string]int64{}
	for _, row := range query(a.readDB(), `SELECT FilePath,ByteOffset FROM LogSyncCursor WHERE ServerID=?`, serverID) {
		cursors[str(row["FilePath"])] = integer(row["ByteOffset"])
	}

	series := boolean(a.setting("session_traffic_enabled"))
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		offset := cursors[target.path]
		if target.size > 0 && target.size == offset {
			continue
		}
		if target.size > 0 && target.size < offset {
			offset = 0
		}

		var buf strings.Builder
		startOffset := offset
		readOffset := offset
		const maxChunksPerTick = 32
		reachedEOF := false
		for chunk := 0; chunk < maxChunksPerTick; chunk++ {
			p := M{"FilePath_str": target.path}
			if readOffset > 0 {
				p["Offset_u32"] = readOffset
			}
			if target.serverName != "" {
				p["ServerName_str"] = target.serverName
			}
			var raw []byte
			ok := false
			func() {
				defer func() { _ = recover() }()
				out := a.rpc(ctx, "ReadLogFile", p)
				decoded, err := base64.StdEncoding.DecodeString(str(out["Buffer_bin"]))
				if err == nil {
					raw = decoded
					ok = true
				}
			}()
			if !ok || len(raw) == 0 {
				reachedEOF = true
				break
			}
			buf.Write(raw)
			readOffset += int64(len(raw))
			if target.size > 0 && readOffset >= target.size {
				reachedEOF = true
				break
			}
		}

		chunkText := buf.String()
		lastNL := strings.LastIndexByte(chunkText, '\n')
		if lastNL < 0 {
			continue
		}
		completeText := chunkText[:lastNL+1]
		newOffset := startOffset + int64(lastNL+1)

		loc := inferLogLocation(completeText, target.updatedAt, serverNowUTC, reachedEOF, calibratedLoc)
		if loc != nil && loc != time.UTC {
			calibratedLoc = loc
		}
		events := parseSecurityLogEvents(target.hub, completeText, loc)
		a.applyLogSessionEvents(ctx, serverID, target.hub, target.path, newOffset, target.size, series, serverNowUTC, events)
	}
}

func (a *App) applyLogSessionEvents(ctx context.Context, serverID int64, hub, filePath string, newOffset, fileSize int64, series bool, nowUTC time.Time, events []parsedLogSession) {
	var ips []string
	for _, ev := range events {
		if ev.clientIP != "" {
			ips = append(ips, ev.clientIP)
		}
	}
	geoMap := a.batchLookupIPs(ctx, ips, true)

	// Fetch cached user metadata if available (no extra RPC).
	a.userMetaMu.RLock()
	cachedMeta := a.userMeta[userMetaCacheKey(serverID, hub)]
	a.userMetaMu.RUnlock()

	tx, err := a.db.Begin()
	if err != nil {
		log.Printf("log sync begin tx %d/%s: %v", serverID, hub, err)
		return
	}
	defer tx.Rollback()

	canonicalNames := map[string]string{}
	resolveUsername := func(raw string) string {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return ""
		}
		low := strings.ToLower(trimmed)
		if u, ok := cachedMeta.users[low]; ok && u.name != "" {
			return u.name
		}
		if name, ok := canonicalNames[low]; ok {
			return name
		}
		var dbName string
		if err := tx.QueryRow(
			`SELECT UserName FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE ORDER BY (UserName != UPPER(UserName)) DESC, VpnSessionSampleID DESC LIMIT 1`,
			serverID, hub, trimmed,
		).Scan(&dbName); err == nil && strings.TrimSpace(dbName) != "" {
			canonicalNames[low] = strings.TrimSpace(dbName)
			return canonicalNames[low]
		}
		canonicalNames[low] = trimmed
		return trimmed
	}

	futureCutoff := nowUTC.Add(5 * time.Minute)
	for _, ev := range events {
		if ev.startedAt.After(futureCutoff) || ev.endedAt.After(futureCutoff) {
			continue
		}
		ev.username = resolveUsername(ev.username)
		startedISO := ev.startedAt.UTC().Format(time.RFC3339Nano)
		endedISO := ev.endedAt.UTC().Format(time.RFC3339Nano)
		startedTs := ev.startedAt.Unix()
		endedTs := ev.endedAt.Unix()
		totalBytes := ev.downBytes + ev.upBytes
		geo := geoMap[cleanIP(ev.clientIP)]
		var realName, note string
		if u, ok := cachedMeta.users[strings.ToLower(strings.TrimSpace(ev.username))]; ok {
			realName, note = u.realName, u.note
		}

		var existingID, existingDown, existingUp, existingTotal int64
		var existingEnded sql.NullString
		var matchErr error
		if ev.hasStart {
			matchErr = tx.QueryRow(
				`SELECT VpnSessionSampleID,EndedDate,DownloadBytes,UploadBytes,BytesTotal FROM VpnSessionSample
				 WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE
				   AND (
				     SessionKey LIKE 'sess:' || ? || '@%'
				     OR SessionKey=?
				     OR (EndedDate IS NULL AND COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) <= ? AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 120)
				     OR (EndedDate IS NOT NULL AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 120 AND ABS(COALESCE(EndedTs, unixepoch(EndedDate)) - ?) <= 360 AND (ClientIP='' OR ?='' OR ClientIP=?))
				   )
				 ORDER BY (EndedDate IS NOT NULL) ASC, ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) ASC LIMIT 1`,
				serverID, hub, ev.username, ev.sessionName,
				"log:"+ev.sessionName+"@"+ev.startedAt.UTC().Format("20060102T150405.000Z"),
				endedTs, startedTs, startedTs, endedTs, ev.clientIP, ev.clientIP, startedTs,
			).Scan(&existingID, &existingEnded, &existingDown, &existingUp, &existingTotal)
		} else {
			// End-only termination log event (e.g. WireGuard where session start didn't match a Connection line):
			// match by session name or recent end time so we enrich the polled row instead of inserting a 0s duplicate.
			matchErr = tx.QueryRow(
				`SELECT VpnSessionSampleID,EndedDate,DownloadBytes,UploadBytes,BytesTotal FROM VpnSessionSample
				 WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE
				   AND (
				     SessionKey LIKE 'sess:' || ? || '@%'
				     OR SessionKey=?
				     OR (EndedDate IS NULL AND COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) <= ? + 30 AND (ClientIP='' OR ?='' OR ClientIP=?))
				     OR (EndedDate IS NOT NULL AND COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) <= ? + 30 AND ABS(COALESCE(EndedTs, unixepoch(EndedDate)) - ?) <= 360 AND (ClientIP='' OR ?='' OR ClientIP=?))
				   )
				 ORDER BY (EndedDate IS NOT NULL) ASC, ABS(COALESCE(EndedTs, unixepoch(COALESCE(EndedDate, LastSeenDate))) - ?) ASC LIMIT 1`,
				serverID, hub, ev.username, ev.sessionName,
				"log:"+ev.sessionName+"@"+ev.startedAt.UTC().Format("20060102T150405.000Z"),
				endedTs, ev.clientIP, ev.clientIP, endedTs, endedTs, ev.clientIP, ev.clientIP, endedTs,
			).Scan(&existingID, &existingEnded, &existingDown, &existingUp, &existingTotal)
		}

		if matchErr == nil && existingID > 0 {
			newDown := max(existingDown, ev.downBytes)
			newUp := max(existingUp, ev.upBytes)
			newTotal := max(existingTotal, max(totalBytes, newDown+newUp))
			_, err = tx.Exec(
				`UPDATE VpnSessionSample SET
				   EndedDate=CASE WHEN EndedDate IS NULL OR (DisconnectReason != 'WireGuard peer inactive timeout' AND julianday(?)>=julianday(EndedDate)) THEN ? ELSE EndedDate END,
				   EndedTs=CASE WHEN EndedTs IS NULL OR (DisconnectReason != 'WireGuard peer inactive timeout' AND ?>=EndedTs) THEN ? ELSE EndedTs END,
				   LastSeenDate=CASE WHEN julianday(?)>julianday(LastSeenDate) THEN ? ELSE LastSeenDate END,
				   DownloadBytes=?, UploadBytes=?, BytesTotal=?,
				   PacketsTotal=MAX(PacketsTotal, ?),
				   ClientIP=CASE WHEN ClientIP='' THEN ? ELSE ClientIP END,
				   VirtualIP=CASE WHEN VirtualIP='' THEN ? ELSE VirtualIP END,
				   ClientHostname=CASE WHEN ClientHostname='' THEN ? ELSE ClientHostname END,
				   ClientPort=CASE WHEN ClientPort=0 THEN ? ELSE ClientPort END,
				   Protocol=CASE WHEN ? != '' AND (Protocol='' OR Protocol='SoftEther SSL-VPN') THEN ? ELSE Protocol END,
				   DisconnectReason=CASE WHEN ? != '' AND DisconnectReason='' THEN ? ELSE DisconnectReason END,
				   RealName=CASE WHEN RealName='' THEN ? ELSE RealName END,
				   Note=CASE WHEN Note='' THEN ? ELSE Note END,
				   IpLocation=CASE WHEN IpLocation='' THEN ? ELSE IpLocation END,
				   IpISP=CASE WHEN IpISP='' THEN ? ELSE IpISP END,
				   IpFull=CASE WHEN IpFull='' THEN ? ELSE IpFull END
				 WHERE VpnSessionSampleID=?`,
				endedISO, endedISO, endedTs, endedTs, endedISO, endedISO,
				newDown, newUp, newTotal, ev.packetsTotal,
				ev.clientIP, ev.virtualIP, ev.clientHost, ev.clientPort, ev.protocol, ev.protocol, ev.disconnectReason, ev.disconnectReason,
				realName, note, geo.Location, geo.ISP, geo.Full, existingID,
			)
			if err != nil {
				log.Printf("log sync update session %d: %v", existingID, err)
				return
			}
			if series && (newDown > existingDown || newUp > existingUp || newTotal > existingTotal) {
				_, _ = tx.Exec(
					`INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate,SampledTs) VALUES(?,?,?,?,?,?)`,
					existingID, newDown, newUp, newTotal, endedISO, endedTs,
				)
			}
			continue
		}

		// Clean up any lower-fidelity synthetic short:% row within ±120s before inserting the authoritative log session.
		_, _ = tx.Exec(
			`DELETE FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE AND SessionKey LIKE 'short:%' AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 120`,
			serverID, hub, ev.username, startedTs,
		)

		logKey := fmt.Sprintf("log:%s@%s", ev.sessionName, ev.startedAt.UTC().Format("20060102T150405.000Z"))
		var insertedID int64
		err = tx.QueryRow(
			`INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,VirtualIP,ClientHostname,StartedDate,EndedDate,LastSeenDate,BytesTotal,PacketsTotal,DownloadBytes,UploadBytes,StartedTs,EndedTs,RealName,Note,IpLocation,IpISP,IpFull,Protocol,DisconnectReason,ClientPort)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			 ON CONFLICT(ServerID,HubName,SessionKey) DO UPDATE SET
			   EndedDate=excluded.EndedDate, EndedTs=excluded.EndedTs, LastSeenDate=excluded.LastSeenDate,
			   BytesTotal=MAX(VpnSessionSample.BytesTotal, excluded.BytesTotal),
			   PacketsTotal=MAX(VpnSessionSample.PacketsTotal, excluded.PacketsTotal),
			   DownloadBytes=MAX(VpnSessionSample.DownloadBytes, excluded.DownloadBytes),
			   UploadBytes=MAX(VpnSessionSample.UploadBytes, excluded.UploadBytes),
			   VirtualIP=CASE WHEN VpnSessionSample.VirtualIP='' THEN excluded.VirtualIP ELSE VpnSessionSample.VirtualIP END,
			   ClientHostname=CASE WHEN VpnSessionSample.ClientHostname='' THEN excluded.ClientHostname ELSE VpnSessionSample.ClientHostname END,
			   Protocol=CASE WHEN excluded.Protocol!='' AND (VpnSessionSample.Protocol='' OR VpnSessionSample.Protocol='SoftEther SSL-VPN') THEN excluded.Protocol ELSE VpnSessionSample.Protocol END,
			   DisconnectReason=CASE WHEN excluded.DisconnectReason!='' THEN excluded.DisconnectReason ELSE VpnSessionSample.DisconnectReason END
			 RETURNING VpnSessionSampleID`,
			serverID, hub, ev.username, logKey, ev.clientIP, ev.virtualIP, ev.clientHost, startedISO, endedISO, endedISO,
			totalBytes, ev.packetsTotal, ev.downBytes, ev.upBytes, startedTs, endedTs,
			realName, note, geo.Location, geo.ISP, geo.Full, ev.protocol, ev.disconnectReason, ev.clientPort,
		).Scan(&insertedID)
		if err != nil {
			log.Printf("log sync insert session %d/%s: %v", serverID, hub, err)
			return
		}
		if series && insertedID > 0 && totalBytes > 0 {
			_, _ = tx.Exec(`INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate,SampledTs) VALUES(?,?,?,?,?,?)`, insertedID, 0, 0, 0, startedISO, startedTs)
			_, _ = tx.Exec(`INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate,SampledTs) VALUES(?,?,?,?,?,?)`, insertedID, ev.downBytes, ev.upBytes, totalBytes, endedISO, endedTs)
		}
	}

	_, err = tx.Exec(
		`INSERT INTO LogSyncCursor(ServerID,HubName,FilePath,ByteOffset,FileSize,UpdatedDate) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(ServerID,HubName,FilePath) DO UPDATE SET ByteOffset=excluded.ByteOffset,FileSize=excluded.FileSize,UpdatedDate=excluded.UpdatedDate`,
		serverID, hub, filePath, newOffset, fileSize, now(),
	)
	if err != nil {
		log.Printf("log sync cursor %d/%s: %v", serverID, hub, err)
		return
	}
	if err = tx.Commit(); err != nil {
		log.Printf("log sync commit %d/%s: %v", serverID, hub, err)
	}
}
