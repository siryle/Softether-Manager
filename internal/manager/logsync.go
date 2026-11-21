package manager

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	reLogTimestamp  = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})(?:\.\d+)?\s+(.*)$`)
	reLogConnStart  = regexp.MustCompile(`Connection "([^"]+)".*?(?:IP address[^0-9a-fA-F:]*|from\s+)([0-9a-fA-F:.]+).*?(?:port(?:\s+number)?(?:\s+is)?[:\s]+)(\d+)`)
	reLogConnAuth   = regexp.MustCompile(`Connection "([^"]+)".*?(?:User authentication succeeded\.?\s*User name:\s*"([^"]+)"|Successfully authenticated as user "([^"]+)"|Authenticated as "([^"]+)")`)
	reLogSessCreate = regexp.MustCompile(`Connection "([^"]+)".*?session "([^"]+)" has been created`)
	reLogSessStart  = regexp.MustCompile(`Session "([^"]+)".*?(?:has been (?:started|established)|logged in)`)
	reLogSessEnd    = regexp.MustCompile(`Session "([^"]+)": The session has been terminated\.(.*)`)
	reLogConnTerm   = regexp.MustCompile(`Connection "([^"]+)" terminated by the cause "([^"]+)"(?:\s*\(code\s*(\d+)\))?`)
	reLogOutBytes   = regexp.MustCompile(`Total outgoing data size:\s*(\d+)\s*bytes`)
	reLogInBytes    = regexp.MustCompile(`Total incoming data size:\s*(\d+)\s*bytes`)
	reLogPackets    = regexp.MustCompile(`Total (?:outgoing|incoming) (?:unicast|broadcast) packets:\s*(\d+)\s*packets`)
	reLogClientIP   = regexp.MustCompile(`(?:IP address(?:\s+of the client)?(?:\s+is)?[:\s]+)([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+|[0-9a-fA-F:]+:[0-9a-fA-F:]+)`)
	reLogPort       = regexp.MustCompile(`[Pp]ort(?:\s+number)?(?:\s+is)?[:\s]+(\d+)`)
	reLogProto      = regexp.MustCompile(`Physical protocol:\s*"([^"]+)"`)
	reLogProduct    = regexp.MustCompile(`Client product name:\s*"([^"]+)"`)
	reLogSecFile    = regexp.MustCompile(`(?i)(?:^|/)security_log/([^/]+)/sec_(\d{8})\.log$`)
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
	clientPort       int64
	protocol         string
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
			protoSrc := M{"Name_str": sn}
			if pm := reLogProto.FindStringSubmatch(body); len(pm) == 2 {
				protoSrc["UnderlayProtocol_str"] = pm[1]
			}
			if prm := reLogProduct.FindStringSubmatch(body); len(prm) == 2 {
				protoSrc["ClientProductName_str"] = prm[1]
			}
			if p := detectSessionProtocol(protoSrc, nil); p != "" {
				c.protocol = p
			}
			user := c.user
			if user == "" {
				user = inferUsernameFromSessionName(sn)
			}
			openSessions[sn] = &parsedLogSession{
				hub:        hub,
				sessionName: sn,
				username:   user,
				clientIP:   c.clientIP,
				clientPort: c.clientPort,
				protocol:   c.protocol,
				startedAt:  at,
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
				openSessions[sn] = &parsedLogSession{
					hub:         hub,
					sessionName: sn,
					username:    user,
					clientIP:    cip,
					clientPort:  cport,
					protocol:    proto,
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
			if !isClientSessionRPC(M{"Username_str": sess.username, "Name_str": sn}) || sess.username == "" {
				continue
			}
			sess.endedAt = at
			if sess.startedAt.IsZero() || sess.endedAt.Before(sess.startedAt) {
				sess.startedAt = sess.endedAt
			}
			// SoftEther server outgoing = client download; incoming = client upload.
			sess.downBytes = outBytes
			sess.upBytes = inBytes
			sess.packetsTotal = pkts
			if sess.disconnectReason == "" {
				if cid := sessToConn[sn]; cid != "" && conns[cid] != nil && conns[cid].cause != "" {
					sess.disconnectReason = conns[cid].cause
				}
			}
			completed = append(completed, *sess)
		}
	}
	return completed
}

func (a *App) syncSessionLogs(ctx context.Context, serverID int64) {
	if !boolean(a.setting("session_log_sync_enabled")) {
		return
	}
	var logList []M
	var loc *time.Location
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
			loc = t.Location()
		}
	}()

	retentionDays := max(int64(1), integer(a.setting("session_history_retention_days")))
	cutoffDay := time.Now().UTC().AddDate(0, 0, -int(retentionDays+1)).Format("20060102")

	type logFileTarget struct {
		path       string
		serverName string
		hub        string
		size       int64
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
		targets = append(targets, logFileTarget{
			path:       path,
			serverName: str(entry["ServerName_str"]),
			hub:        m[1],
			size:       size,
		})
	}
	if len(targets) == 0 {
		return
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].path < targets[j].path })

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
				break
			}
			buf.Write(raw)
			readOffset += int64(len(raw))
			if target.size > 0 && readOffset >= target.size {
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

		events := parseSecurityLogEvents(target.hub, completeText, loc)
		a.applyLogSessionEvents(ctx, serverID, target.hub, target.path, newOffset, target.size, series, events)
	}
}

func (a *App) applyLogSessionEvents(ctx context.Context, serverID int64, hub, filePath string, newOffset, fileSize int64, series bool, events []parsedLogSession) {
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

	for _, ev := range events {
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
		err := tx.QueryRow(
			`SELECT VpnSessionSampleID,EndedDate,DownloadBytes,UploadBytes,BytesTotal FROM VpnSessionSample
			 WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE
			   AND (
			     SessionKey LIKE 'sess:' || ? || '@%'
			     OR SessionKey=?
			     OR (EndedDate IS NULL AND COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) <= ? AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 120)
			     OR (EndedDate IS NOT NULL AND ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) <= 15 AND ABS(COALESCE(EndedTs, unixepoch(EndedDate)) - ?) <= 90)
			   )
			 ORDER BY (EndedDate IS NOT NULL) ASC, ABS(COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) - ?) ASC LIMIT 1`,
			serverID, hub, ev.username, ev.sessionName,
			"log:"+ev.sessionName+"@"+ev.startedAt.UTC().Format("20060102T150405.000Z"),
			endedTs, startedTs, startedTs, endedTs, startedTs,
		).Scan(&existingID, &existingEnded, &existingDown, &existingUp, &existingTotal)

		if err == nil && existingID > 0 {
			newDown := max(existingDown, ev.downBytes)
			newUp := max(existingUp, ev.upBytes)
			newTotal := max(existingTotal, max(totalBytes, newDown+newUp))
			_, err = tx.Exec(
				`UPDATE VpnSessionSample SET
				   EndedDate=CASE WHEN EndedDate IS NULL OR julianday(?)>=julianday(EndedDate) THEN ? ELSE EndedDate END,
				   EndedTs=CASE WHEN EndedTs IS NULL OR ?>=EndedTs THEN ? ELSE EndedTs END,
				   LastSeenDate=CASE WHEN julianday(?)>julianday(LastSeenDate) THEN ? ELSE LastSeenDate END,
				   DownloadBytes=?, UploadBytes=?, BytesTotal=?,
				   PacketsTotal=MAX(PacketsTotal, ?),
				   ClientIP=CASE WHEN ClientIP='' THEN ? ELSE ClientIP END,
				   ClientPort=CASE WHEN ClientPort=0 THEN ? ELSE ClientPort END,
				   Protocol=CASE WHEN Protocol='' THEN ? ELSE Protocol END,
				   DisconnectReason=CASE WHEN ? != '' THEN ? ELSE DisconnectReason END,
				   RealName=CASE WHEN RealName='' THEN ? ELSE RealName END,
				   Note=CASE WHEN Note='' THEN ? ELSE Note END,
				   IpLocation=CASE WHEN IpLocation='' THEN ? ELSE IpLocation END,
				   IpISP=CASE WHEN IpISP='' THEN ? ELSE IpISP END,
				   IpFull=CASE WHEN IpFull='' THEN ? ELSE IpFull END
				 WHERE VpnSessionSampleID=?`,
				endedISO, endedISO, endedTs, endedTs, endedISO, endedISO,
				newDown, newUp, newTotal, ev.packetsTotal,
				ev.clientIP, ev.clientPort, ev.protocol, ev.disconnectReason, ev.disconnectReason,
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
			   DisconnectReason=CASE WHEN excluded.DisconnectReason!='' THEN excluded.DisconnectReason ELSE VpnSessionSample.DisconnectReason END
			 RETURNING VpnSessionSampleID`,
			serverID, hub, ev.username, logKey, ev.clientIP, "", "", startedISO, endedISO, endedISO,
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
