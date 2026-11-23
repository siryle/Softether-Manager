package manager

import (
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"time"
)

func (a *App) serverAnalytics(serverID int64, hub string, hours int64, rangeKind string, loc *time.Location) M {
	rdb := a.readDB()
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	useRollup := true
	if rangeKind == "today" {
		if loc == nil {
			loc = time.Local
		}
		nowLocal := end.In(loc)
		start = time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, loc).UTC()
		// Only use hourly rollup when local midnight aligns cleanly to an hourly UTC bucket.
		useRollup = start.Unix()%3600 == 0
	}
	startBucket := start.Unix() - (start.Unix() % 3600)
	endTs := end.Unix()

	// 1. Top users by traffic in the requested window (prefer TrafficRollupHourly, fallback to UserTrafficSample).
	type userAgg struct {
		hub      string
		username string
		realName string
		send     int64
		recv     int64
	}
	topUsers := []M{}
	{
		var rows []M
		if useRollup {
			where := `ServerID=? AND SubjectType='user' AND BucketTs>=? AND BucketTs<=?`
			args := []any{serverID, startBucket, endTs}
			if hub != "" {
				where += ` AND HubName=?`
				args = append(args, hub)
			}
			rows = query(rdb, `SELECT HubName hub, COALESCE(NULLIF(MAX(UserName),''), UserKey) username, SUM(SendBytes) send_bytes, SUM(RecvBytes) recv_bytes FROM TrafficRollupHourly WHERE `+where+` GROUP BY HubName, UserKey HAVING SUM(SendBytes)+SUM(RecvBytes)>0 ORDER BY (SUM(SendBytes)+SUM(RecvBytes)) DESC LIMIT 8`, args...)
		}
		if len(rows) == 0 {
			// Fallback to raw UserTrafficSample with HasDelta=1 or session totals if rollup hasn't accumulated yet.
			rawWhere := `ServerID=? AND HasDelta=1 AND ((SampledTs>0 AND SampledTs>=? AND SampledTs<=?) OR (SampledTs=0 AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<=julianday(?)))`
			rawArgs := []any{serverID, start.Unix(), endTs, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
			if hub != "" {
				rawWhere += ` AND HubName=?`
				rawArgs = append(rawArgs, hub)
			}
			rows = query(rdb, `SELECT HubName hub, UserName username, SUM(DeltaSendBytes) send_bytes, SUM(DeltaRecvBytes) recv_bytes FROM UserTrafficSample WHERE `+rawWhere+` GROUP BY HubName, LOWER(UserName) HAVING SUM(DeltaSendBytes)+SUM(DeltaRecvBytes)>0 ORDER BY (SUM(DeltaSendBytes)+SUM(DeltaRecvBytes)) DESC LIMIT 8`, rawArgs...)
		}
		if len(rows) == 0 {
			// Final fallback: aggregate client sessions in VpnSessionSample in the window.
			sessWhere := `ServerID=? AND ` + clientSessionSQLClause + ` AND ((StartedTs>0 AND StartedTs>=? AND StartedTs<=?) OR (StartedTs=0 AND julianday(StartedDate)>=julianday(?) AND julianday(StartedDate)<=julianday(?)))`
			sessArgs := []any{serverID, start.Unix(), endTs, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
			if hub != "" {
				sessWhere += ` AND HubName=?`
				sessArgs = append(sessArgs, hub)
			}
			rows = query(rdb, `SELECT HubName hub, UserName username, MAX(RealName) real_name, SUM(UploadBytes) send_bytes, SUM(DownloadBytes) recv_bytes FROM VpnSessionSample WHERE `+sessWhere+` GROUP BY HubName, LOWER(UserName) HAVING SUM(BytesTotal)>0 ORDER BY SUM(BytesTotal) DESC LIMIT 8`, sessArgs...)
		}
		var listAgg []userAgg
		for _, r := range rows {
			u := userAgg{
				hub:      str(r["hub"]),
				username: str(r["username"]),
				realName: str(r["real_name"]),
				send:     integer(r["send_bytes"]),
				recv:     integer(r["recv_bytes"]),
			}
			if u.realName == "" {
				a.userMetaMu.RLock()
				if cached, ok := a.userMeta[userMetaCacheKey(serverID, u.hub)]; ok {
					if m, ok := cached.users[strings.ToLower(strings.TrimSpace(u.username))]; ok {
						u.realName = m.realName
					}
				}
				a.userMetaMu.RUnlock()
			}
			listAgg = append(listAgg, u)
		}
		for _, u := range listAgg {
			topUsers = append(topUsers, M{
				"hub":         u.hub,
				"username":    u.username,
				"real_name":   u.realName,
				"send_bytes":  u.send,
				"recv_bytes":  u.recv,
				"total_bytes": u.send + u.recv,
			})
		}
	}

	// 2. Per-hub traffic totals in window.
	hubTotals := []M{}
	{
		var rows []M
		if useRollup {
			where := `ServerID=? AND SubjectType='hub' AND BucketTs>=? AND BucketTs<=?`
			args := []any{serverID, startBucket, endTs}
			if hub != "" {
				where += ` AND HubName=?`
				args = append(args, hub)
			}
			rows = query(rdb, `SELECT HubName hub, SUM(SendBytes) send_bytes, SUM(RecvBytes) recv_bytes, MAX(MaxSessions) max_sessions FROM TrafficRollupHourly WHERE `+where+` GROUP BY HubName ORDER BY (SUM(SendBytes)+SUM(RecvBytes)) DESC`, args...)
		}
		if len(rows) == 0 {
			rawWhere := `ServerID=? AND HasDelta=1 AND ((SampledTs>0 AND SampledTs>=? AND SampledTs<=?) OR (SampledTs=0 AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<=julianday(?)))`
			rawArgs := []any{serverID, start.Unix(), endTs, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
			if hub != "" {
				rawWhere += ` AND HubName=?`
				rawArgs = append(rawArgs, hub)
			}
			rows = query(rdb, `SELECT HubName hub, SUM(DeltaSendBytes) send_bytes, SUM(DeltaRecvBytes) recv_bytes, MAX(NumSessions) max_sessions FROM HubTrafficSample WHERE `+rawWhere+` GROUP BY HubName ORDER BY (SUM(DeltaSendBytes)+SUM(DeltaRecvBytes)) DESC`, rawArgs...)
		}
		for _, r := range rows {
			s := integer(r["send_bytes"])
			rc := integer(r["recv_bytes"])
			hubTotals = append(hubTotals, M{
				"hub":          str(r["hub"]),
				"send_bytes":   s,
				"recv_bytes":   rc,
				"total_bytes":  s + rc,
				"max_sessions": integer(r["max_sessions"]),
			})
		}
	}

	// 3. Active protocol breakdown from currently open client sessions.
	protocolCounts := []M{}
	{
		where := `ServerID=? AND EndedDate IS NULL AND ` + clientSessionSQLClause
		args := []any{serverID}
		if hub != "" {
			where += ` AND HubName=?`
			args = append(args, hub)
		}
		rows := query(rdb, `SELECT CASE WHEN Protocol='' THEN 'Unknown' ELSE Protocol END protocol, COUNT(*) count FROM VpnSessionSample WHERE `+where+` GROUP BY CASE WHEN Protocol='' THEN 'Unknown' ELSE Protocol END ORDER BY count DESC`, args...)
		for _, r := range rows {
			protocolCounts = append(protocolCounts, M{
				"protocol": str(r["protocol"]),
				"count":    integer(r["count"]),
			})
		}
	}

	return M{
		"hours":           hours,
		"hub":             hub,
		"top_users":       topUsers,
		"hub_totals":      hubTotals,
		"protocol_counts": protocolCounts,
	}
}

func (a *App) sessionReportAnalytics(serverID int64, r *http.Request) M {
	rdb := a.readDB()
	where, args := buildSessionReportFilter(serverID, r)
	loc := resolveExportLocation(r)

	// Decide hourly vs daily bucketing based on query range (default daily, hourly if <= 48h).
	fromRaw := parseTimeQuery(r.URL.Query().Get("from"), false)
	toRaw := parseTimeQuery(r.URL.Query().Get("to"), true)
	hourly := false
	if fromRaw != "" {
		fromT := parseTime(fromRaw)
		toT := time.Now().UTC()
		if toRaw != "" {
			if pt := parseTime(toRaw); !pt.IsZero() {
				toT = pt
			}
		}
		if !fromT.IsZero() && toT.Sub(fromT) <= 48*time.Hour && toT.Sub(fromT) >= 0 {
			hourly = true
		}
	}

	// 1. Scan lightweight columns to build local-timezone trend buckets + anomalies accurately.
	type bucketAgg struct {
		key         string
		label       string
		sessions    int64
		users       map[string]struct{}
		totalBytes  int64
		downBytes   int64
		upBytes     int64
	}
	bucketMap := map[string]*bucketAgg{}
	var abnormalDisconnects, shortSessions int64
	activeUserIPs := map[string]map[string]struct{}{}

	rows, err := rdb.Query(
		`SELECT UserName, ClientIP, StartedDate, EndedDate, StartedTs, EndedTs, BytesTotal, DownloadBytes, UploadBytes, DisconnectReason FROM VpnSessionSample WHERE `+where,
		args...,
	)
	check(err)
	defer rows.Close()

	for rows.Next() {
		var uname, cip, startedRaw, reason string
		var endedRaw sql.NullString
		var startedTs int64
		var endedTs sql.NullInt64
		var totalB, downB, upB int64
		check(rows.Scan(&uname, &cip, &startedRaw, &endedRaw, &startedTs, &endedTs, &totalB, &downB, &upB, &reason))

		st := parseTime(startedRaw)
		if st.IsZero() && startedTs > 0 {
			st = time.Unix(startedTs, 0).UTC()
		}
		if !st.IsZero() {
			localT := st.In(loc)
			var key, label string
			if hourly {
				key = localT.Format("2006-01-02T15")
				label = localT.Format("15:00")
			} else {
				key = localT.Format("2006-01-02")
				label = localT.Format("01/02")
			}
			b := bucketMap[key]
			if b == nil {
				b = &bucketAgg{key: key, label: label, users: map[string]struct{}{}}
				bucketMap[key] = b
			}
			b.sessions++
			b.users[strings.ToLower(strings.TrimSpace(uname))] = struct{}{}
			b.totalBytes += totalB
			b.downBytes += downB
			b.upBytes += upB
		}

		// Abnormal disconnect check
		lowReason := strings.ToLower(strings.TrimSpace(reason))
		if lowReason != "" && !strings.Contains(lowReason, "no error") && !strings.Contains(lowReason, "code 0") {
			abnormalDisconnects++
		}

		// Short session check (<= 60s)
		if endedRaw.Valid && strings.TrimSpace(endedRaw.String) != "" {
			sSec := startedTs
			if sSec == 0 && !st.IsZero() {
				sSec = st.Unix()
			}
			var eSec int64
			if endedTs.Valid && endedTs.Int64 > 0 {
				eSec = endedTs.Int64
			} else if et := parseTime(endedRaw.String); !et.IsZero() {
				eSec = et.Unix()
			}
			if sSec > 0 && eSec >= sSec && eSec-sSec <= 60 {
				shortSessions++
			}
		} else if strings.TrimSpace(cip) != "" {
			uKey := strings.ToLower(strings.TrimSpace(uname))
			if activeUserIPs[uKey] == nil {
				activeUserIPs[uKey] = map[string]struct{}{}
			}
			activeUserIPs[uKey][cleanIP(cip)] = struct{}{}
		}
	}
	check(rows.Err())

	var concurrentMultiIP int64
	for _, ips := range activeUserIPs {
		if len(ips) >= 2 {
			concurrentMultiIP++
		}
	}

	keys := make([]string, 0, len(bucketMap))
	for k := range bucketMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 31 {
		keys = keys[len(keys)-31:]
	}
	buckets := make([]M, 0, len(keys))
	for _, k := range keys {
		b := bucketMap[k]
		buckets = append(buckets, M{
			"key":            b.key,
			"label":          b.label,
			"sessions":       b.sessions,
			"unique_users":   len(b.users),
			"total_bytes":    b.totalBytes,
			"download_bytes": b.downBytes,
			"upload_bytes":   b.upBytes,
		})
	}

	// 2. Top Users breakdown.
	topUsers := query(rdb, `SELECT HubName hub, UserName username, MAX(RealName) real_name, COUNT(*) sessions, COALESCE(SUM(BytesTotal),0) total_bytes, COALESCE(SUM(DownloadBytes),0) download_bytes, COALESCE(SUM(UploadBytes),0) upload_bytes FROM VpnSessionSample WHERE `+where+` GROUP BY HubName, LOWER(UserName) ORDER BY total_bytes DESC, sessions DESC LIMIT 6`, args...)

	// 3. Top Locations breakdown.
	topLocations := query(rdb, `SELECT CASE WHEN IpLocation='' THEN 'Unknown' ELSE IpLocation END location, COUNT(*) sessions, COALESCE(SUM(BytesTotal),0) total_bytes FROM VpnSessionSample WHERE `+where+` GROUP BY CASE WHEN IpLocation='' THEN 'Unknown' ELSE IpLocation END ORDER BY sessions DESC, total_bytes DESC LIMIT 6`, args...)

	// 4. Protocols breakdown.
	protocols := query(rdb, `SELECT CASE WHEN Protocol='' THEN 'Unknown' ELSE Protocol END protocol, COUNT(*) sessions, COALESCE(SUM(BytesTotal),0) total_bytes FROM VpnSessionSample WHERE `+where+` GROUP BY CASE WHEN Protocol='' THEN 'Unknown' ELSE Protocol END ORDER BY sessions DESC, total_bytes DESC LIMIT 8`, args...)

	return M{
		"granularity":   map[bool]string{true: "hour", false: "day"}[hourly],
		"buckets":       buckets,
		"top_users":     topUsers,
		"top_locations": topLocations,
		"protocols":     protocols,
		"anomalies": M{
			"abnormal_disconnects": abnormalDisconnects,
			"short_sessions":       shortSessions,
			"concurrent_multi_ip":  concurrentMultiIP,
		},
	}
}
