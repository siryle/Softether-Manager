package manager

import (
	"context"
	"encoding/csv"
	"fmt"
	"golang.org/x/text/cases"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func fold(s string) string { return cases.Fold().String(s) }
func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
func delta(current, previous int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}
func usage(rows []M, hours int64, sendKey, recvKey string, sessions bool) M {
	points := []M{}
	var sent, recv int64
	for i := 1; i < len(rows); i++ {
		s := delta(integer(rows[i][sendKey]), integer(rows[i-1][sendKey]))
		r := delta(integer(rows[i][recvKey]), integer(rows[i-1][recvKey]))
		sent += s
		recv += r
		p := M{"t": rows[i]["SampledDate"], "send": s, "recv": r}
		if sessions {
			p["sessions"] = integer(rows[i]["NumSessions"])
		}
		points = append(points, p)
	}
	return M{"hours": hours, "points": points, "total_send": sent, "total_recv": recv, "samples": len(rows)}
}
func (a *App) enrichSessionRowsGeo(ctx context.Context, rows []M) {
	if len(rows) == 0 {
		return
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		if ip := str(row["client_ip"]); ip != "" {
			ips = append(ips, ip)
		}
	}
	geoMap := a.batchLookupIPs(ctx, ips)
	for _, row := range rows {
		geo := geoMap[cleanIP(str(row["client_ip"]))]
		row["ip_location"] = geo.Location
		row["ip_isp"] = geo.ISP
		row["ip_full"] = geo.Full
	}
}

func (a *App) enrichSessionRowsUser(ctx context.Context, rows []M) {
	if len(rows) == 0 {
		return
	}
	hubSet := make(map[string]struct{})
	for _, row := range rows {
		if h := str(row["hub"]); h != "" {
			hubSet[h] = struct{}{}
		}
	}
	userMap := make(map[string]M)
	for hub := range hubSet {
		func() {
			defer func() { _ = recover() }()
			out := a.rpc(ctx, "EnumUser", M{"HubName_str": hub})
			for _, u := range list(out["UserList"]) {
				name := strings.ToLower(strings.TrimSpace(str(u["Name_str"])))
				if name != "" {
					userMap[strings.ToLower(hub)+":"+name] = u
				}
			}
		}()
	}
	for _, row := range rows {
		hub := strings.ToLower(str(row["hub"]))
		username := strings.ToLower(strings.TrimSpace(str(row["username"])))
		if u, ok := userMap[hub+":"+username]; ok {
			row["real_name"] = str(u["Realname_utf"])
			row["note"] = str(u["Note_utf"])
		} else {
			if _, exists := row["real_name"]; !exists {
				row["real_name"] = ""
			}
			if _, exists := row["note"]; !exists {
				row["note"] = ""
			}
		}
	}
}

func parseTimeQuery(raw string, endOfDay bool) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC().Format(time.RFC3339Nano)
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		if endOfDay {
			t = t.Add(24*time.Hour - time.Nanosecond)
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	fail(422, "Invalid time filter.")
	return ""
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

const clientSessionSQLClause = `LOWER(UserName) NOT IN ('securenat', 'local bridge') AND LOWER(UserName) NOT LIKE 'sid-local-bridge%' AND LOWER(UserName) NOT LIKE 'link_%' AND LOWER(UserName) NOT LIKE 'l3_%'`
const nonClientSessionDeleteClause = `LOWER(UserName) IN ('securenat', 'local bridge') OR LOWER(UserName) LIKE 'sid-local-bridge%' OR LOWER(UserName) LIKE 'link_%' OR LOWER(UserName) LIKE 'l3_%'`

func buildSessionReportFilter(serverID int64, r *http.Request) (string, []any) {
	q := r.URL.Query()
	clauses := []string{"ServerID=?", clientSessionSQLClause}
	args := []any{serverID}
	if from := parseTimeQuery(q.Get("from"), false); from != "" {
		clauses = append(clauses, "julianday(StartedDate)>=julianday(?)")
		args = append(args, from)
	}
	if to := parseTimeQuery(q.Get("to"), true); to != "" {
		clauses = append(clauses, "julianday(StartedDate)<=julianday(?)")
		args = append(args, to)
	}
	if hub := strings.TrimSpace(q.Get("hub")); hub != "" {
		clauses = append(clauses, "HubName=?")
		args = append(args, hub)
	}
	switch strings.TrimSpace(q.Get("status")) {
	case "", "all":
	case "active":
		clauses = append(clauses, "EndedDate IS NULL")
	case "ended":
		clauses = append(clauses, "EndedDate IS NOT NULL")
	default:
		fail(422, "Invalid status filter.")
	}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		pat := "%" + escapeLike(search) + "%"
		clauses = append(clauses, `(UserName LIKE ? ESCAPE '\' OR ClientIP LIKE ? ESCAPE '\' OR VirtualIP LIKE ? ESCAPE '\' OR ClientHostname LIKE ? ESCAPE '\' OR HubName LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat, pat, pat)
	}
	return strings.Join(clauses, " AND "), args
}

func safeCSVCell(s string) string {
	if s == "" {
		return ""
	}
	switch s[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + s
	}
	return s
}

func resolveExportLocation(r *http.Request) *time.Location {
	if tz := strings.TrimSpace(r.URL.Query().Get("tz")); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("tz_offset")); raw != "" {
		if mins, err := strconv.Atoi(raw); err == nil && mins >= -840 && mins <= 840 {
			return time.FixedZone("", -mins*60)
		}
	}
	return time.Local
}

func formatExportTime(iso string, loc *time.Location) string {
	if iso == "" {
		return ""
	}
	t := parseTime(iso)
	if t.IsZero() {
		return iso
	}
	return t.In(loc).Format("2006-01-02 15:04:05")
}

func formatExportDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	d := seconds / 86400
	h := (seconds % 86400) / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func (a *App) registerHistory() {
	for _, kind := range []string{"server", "hub", "user"} {
		path := "/server/traffic"
		if kind == "hub" {
			path = "/hubs/{hub}/traffic"
		}
		if kind == "user" {
			path = "/hubs/{hub}/users/{name}/usage"
		}
		a.endpoint("GET "+path, func(_ http.ResponseWriter, r *http.Request, _ M) any {
			hours := queryInt(r, "hours", 24, 1, 2160)
			return a.trafficUsage(a.serverIDFromContext(r.Context()), kind, r.PathValue("hub"), r.PathValue("name"), hours)
		})
	}
	a.endpoint("GET /server/session-history", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		where, args := buildSessionReportFilter(serverID, r)
		limit := queryInt(r, "limit", 100, 1, 500)
		offset := queryInt(r, "offset", 0, 0, 1000000)

		summary := one(a.db, `SELECT COUNT(*) total_sessions, COALESCE(SUM(CASE WHEN EndedDate IS NULL THEN 1 ELSE 0 END), 0) active_sessions, COUNT(DISTINCT LOWER(UserName)) unique_users, COALESCE(SUM(BytesTotal), 0) total_bytes, COALESCE(SUM(DownloadBytes), 0) download_bytes, COALESCE(SUM(UploadBytes), 0) upload_bytes FROM VpnSessionSample WHERE `+where, args...)
		if summary == nil {
			summary = M{"total_sessions": 0, "active_sessions": 0, "unique_users": 0, "total_bytes": 0, "download_bytes": 0, "upload_bytes": 0}
		}

		hubRows := query(a.db, `SELECT DISTINCT HubName hub FROM VpnSessionSample WHERE ServerID=? AND `+clientSessionSQLClause+` ORDER BY HubName`, serverID)
		hubs := make([]string, 0, len(hubRows))
		for _, hr := range hubRows {
			if h := str(hr["hub"]); h != "" {
				hubs = append(hubs, h)
			}
		}

		itemArgs := append(append([]any{}, args...), limit, offset)
		rows := query(a.db, `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE `+where+` ORDER BY julianday(StartedDate) DESC, VpnSessionSampleID DESC LIMIT ? OFFSET ?`, itemArgs...)
		a.enrichSessionRowsGeo(r.Context(), rows)
		a.enrichSessionRowsUser(r.Context(), rows)
		return M{"items": rows, "total": integer(summary["total_sessions"]), "summary": summary, "hubs": hubs}
	})
	a.endpoint("GET /server/session-history/export", func(w http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		where, args := buildSessionReportFilter(serverID, r)
		loc := resolveExportLocation(r)
		zh := strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lang"))), "zh")

		rows := query(a.db, `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE `+where+` ORDER BY julianday(StartedDate) DESC, VpnSessionSampleID DESC LIMIT 50000`, args...)
		a.enrichSessionRowsGeo(r.Context(), rows)
		a.enrichSessionRowsUser(r.Context(), rows)

		filename := fmt.Sprintf("session-audit-report-%s.csv", time.Now().In(loc).Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("\xef\xbb\xbf"))

		cw := csv.NewWriter(w)
		headers := []string{"Session ID", "User", "Virtual Hub", "Status", "Started", "Ended", "Duration (s)", "Duration", "Client IP", "IP Location", "ISP", "Virtual IP", "Hostname", "Download (Bytes)", "Upload (Bytes)", "Total (Bytes)", "Packets"}
		activeText, endedText := "Active", "Ended"
		if zh {
			headers = []string{"会话 ID", "用户名", "虚拟 HUB", "状态", "上线时间", "下线时间", "在线时长(秒)", "在线时长", "客户端 IP", "IP 属地", "运营商", "虚拟 IP", "主机名", "下载流量(Bytes)", "上传流量(Bytes)", "总流量(Bytes)", "数据包"}
			activeText, endedText = "在线中", "已下线"
		}
		_ = cw.Write(headers)

		nowTime := time.Now()
		for _, row := range rows {
			startedRaw := str(row["started_date"])
			endedRaw := str(row["ended_date"])
			startedAt := parseTime(startedRaw)
			endAt := nowTime
			statusText := activeText
			endedCell := ""
			if endedRaw != "" {
				statusText = endedText
				endedCell = formatExportTime(endedRaw, loc)
				if parsedEnd := parseTime(endedRaw); !parsedEnd.IsZero() {
					endAt = parsedEnd
				}
			}
			var durSec int64
			if !startedAt.IsZero() {
				durSec = max(int64(0), int64(endAt.Sub(startedAt).Seconds()))
			}
			_ = cw.Write([]string{
				strconv.FormatInt(integer(row["id"]), 10),
				safeCSVCell(str(row["username"])),
				safeCSVCell(str(row["hub"])),
				statusText,
				formatExportTime(startedRaw, loc),
				endedCell,
				strconv.FormatInt(durSec, 10),
				formatExportDuration(durSec),
				safeCSVCell(str(row["client_ip"])),
				safeCSVCell(str(row["ip_location"])),
				safeCSVCell(str(row["ip_isp"])),
				safeCSVCell(str(row["virtual_ip"])),
				safeCSVCell(str(row["client_hostname"])),
				strconv.FormatInt(integer(row["download_bytes"]), 10),
				strconv.FormatInt(integer(row["upload_bytes"]), 10),
				strconv.FormatInt(integer(row["bytes_total"]), 10),
				strconv.FormatInt(integer(row["packets_total"]), 10),
			})
		}
		cw.Flush()
		return nil
	})
	for _, userOnly := range []bool{false, true} {
		path := "/hubs/{hub}/session-history"
		if userOnly {
			path = "/hubs/{hub}/users/{name}/session-history"
		}
		a.endpoint("GET "+path, func(_ http.ResponseWriter, r *http.Request, _ M) any {
			serverID := a.serverIDFromContext(r.Context())
			statement := `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND ` + clientSessionSQLClause
			args := []any{serverID, r.PathValue("hub")}
			if userOnly {
				statement += " AND UserName=? COLLATE NOCASE"
				args = append(args, r.PathValue("name"))
			}
			if before := queryInt(r, "before_id", 0, 0, 1<<63-1); before > 0 {
				statement += " AND VpnSessionSampleID<?"
				args = append(args, before)
			}
			statement += " ORDER BY VpnSessionSampleID DESC LIMIT ?"
			args = append(args, queryInt(r, "limit", 100, 1, 500))
			rows := query(a.db, statement, args...)
			a.enrichSessionRowsGeo(r.Context(), rows)
			a.enrichSessionRowsUser(r.Context(), rows)
			return rows
		})
	}
	a.endpoint("GET /hubs/{hub}/session-history/{session_id}/usage", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		session := one(a.db, `SELECT VpnSessionSampleID id,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE ServerID=? AND VpnSessionSampleID=? AND HubName=? AND `+clientSessionSQLClause, serverID, r.PathValue("session_id"), r.PathValue("hub"))
		if session == nil {
			fail(404, "No such session in this hub's history.")
		}
		geo := a.lookupIP(r.Context(), str(session["client_ip"]))
		session["ip_location"] = geo.Location
		session["ip_isp"] = geo.ISP
		session["ip_full"] = geo.Full
		rows := query(a.db, `SELECT DownloadBytes,UploadBytes,TotalBytes,SampledDate FROM VpnSessionTrafficSample WHERE VpnSessionSampleID=? ORDER BY SampledDate`, session["id"])
		end := time.Now()
		if s := str(session["ended_date"]); s != "" {
			end = parseTime(s)
		}
		hours := max(int64(1), int64(end.Sub(parseTime(str(session["started_date"]))).Hours()+0.5))
		out := usage(rows, hours, "UploadBytes", "DownloadBytes", false)
		out["start"] = parseTime(str(session["started_date"])).UTC().Format(time.RFC3339Nano)
		out["end"] = end.UTC().Format(time.RFC3339Nano)
		out["sample_interval_seconds"] = max(int64(5), integer(a.setting("session_interval_seconds")))
		out["session"] = session
		combined := []M{}
		var total int64
		for i := 1; i < len(rows); i++ {
			d := delta(integer(rows[i]["TotalBytes"]), integer(rows[i-1]["TotalBytes"]))
			total += d
			combined = append(combined, M{"t": rows[i]["SampledDate"], "total": d})
		}
		out["combined"], out["total_combined"], out["split_available"] = combined, total, integer(out["total_send"])+integer(out["total_recv"]) > 0
		return out
	})
}
func (a *App) worker(ctx context.Context, initial time.Duration, interval func() time.Duration, fn func(context.Context)) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		timer := time.NewTimer(initial)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				delay := time.Minute
				func() {
					defer func() {
						if p := recover(); p != nil {
							log.Printf("background task: %v", p)
						}
					}()
					fn(ctx)
					delay = max(time.Second, interval())
				}()
				timer.Reset(delay)
			}
		}
	}()
}
func (a *App) startWorkers(ctx context.Context) {
	a.worker(ctx, 15*time.Second, func() time.Duration {
		return time.Duration(max(int64(1), integer(a.setting("sample_interval_minutes")))) * time.Minute
	}, a.sampleTraffic)
	a.worker(ctx, 15*time.Second, func() time.Duration {
		return time.Duration(max(int64(5), integer(a.setting("session_interval_seconds")))) * time.Second
	}, a.sampleSessions)
	a.worker(ctx, 15*time.Second, func() time.Duration {
		return time.Duration(max(int64(10), integer(a.setting("quota_interval_seconds")))) * time.Second
	}, a.quotaTick)
	a.worker(ctx, time.Millisecond, func() time.Duration {
		return time.Duration(max(int64(1), integer(a.setting("resource_interval_seconds")))) * time.Second
	}, a.sampleResources)
	a.worker(ctx, 30*time.Second, func() time.Duration {
		return time.Minute
	}, a.backupTick)
	a.startGeoWorker(ctx)
}
func bytesFor(m M, prefix string) (int64, int64) {
	return integer(m[prefix+"Send.UnicastBytes_u64"]) + integer(m[prefix+"Send.BroadcastBytes_u64"]), integer(m[prefix+"Recv.UnicastBytes_u64"]) + integer(m[prefix+"Recv.BroadcastBytes_u64"])
}
func (a *App) prune() {
	// Purge any legacy non-client session records (e.g. SecureNAT, Local Bridge, Cascade, L3 switch)
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE VpnSessionSampleID IN (SELECT VpnSessionSampleID FROM VpnSessionSample WHERE `+nonClientSessionDeleteClause+`)`)
	execute(a.db, `DELETE FROM VpnSessionSample WHERE `+nonClientSessionDeleteClause)

	days := max(int64(1), integer(a.setting("sample_retention_days")))
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample"} {
		execute(a.db, `DELETE FROM `+table+` WHERE datetime(SampledDate)<datetime('now',?)`, fmt.Sprintf("-%d days", days))
	}
	days = max(int64(1), integer(a.setting("session_history_retention_days")))
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE datetime(SampledDate)<datetime('now',?)`, fmt.Sprintf("-%d days", days))
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE VpnSessionSampleID IN (SELECT VpnSessionSampleID FROM VpnSessionSample WHERE datetime(COALESCE(EndedDate,LastSeenDate))<datetime('now',?))`, fmt.Sprintf("-%d days", days))
	execute(a.db, `DELETE FROM VpnSessionSample WHERE datetime(COALESCE(EndedDate,LastSeenDate))<datetime('now',?)`, fmt.Sprintf("-%d days", days))
}
func (a *App) sampleTraffic(ctx context.Context) {
	a.prune()
	if !boolean(a.setting("traffic_monitor_enabled")) {
		return
	}
	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	if len(servers) == 0 {
		return
	}
	stamp := now()
	for _, s := range servers {
		serverID := integer(s["ServerID"])
		serverCtx := context.WithValue(ctx, serverIDKey, serverID)
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("traffic sample server %d: %v", serverID, p)
				}
			}()
			type hubSampleRow struct {
				hub            string
				send, recv     int64
				sessions, users int64
			}
			type userSampleRow struct {
				hub, name  string
				send, recv int64
				numLogin   int64
			}
			var hubSamples []hubSampleRow
			var userSamples []userSampleRow
			for _, h := range list(a.rpc(serverCtx, "EnumHub", nil)["HubList"]) {
				hub := str(h["HubName_str"])
				if hub == "" {
					continue
				}
				func() {
					defer func() {
						if p := recover(); p != nil {
							log.Printf("traffic sample %d/%s: %v", serverID, hub, p)
						}
					}()
					status := a.rpc(serverCtx, "GetHubStatus", M{"HubName_str": hub})
					if send, recv, valid := sampledTraffic(status, ""); valid {
						hubSamples = append(hubSamples, hubSampleRow{
							hub:      hub,
							send:     send,
							recv:     recv,
							sessions: integer(status["NumSessions_u32"]),
							users:    integer(status["NumUsers_u32"]),
						})
					} else {
						log.Printf("traffic sample %d/%s: missing or invalid hub counters", serverID, hub)
					}
					for _, u := range list(a.rpc(serverCtx, "EnumUser", M{"HubName_str": hub})["UserList"]) {
						name := str(u["Name_str"])
						sBytes, rBytes, valid := sampledTraffic(u, "Ex.")
						if !valid {
							log.Printf("traffic sample %d/%s/%s: missing or invalid user counters", serverID, hub, name)
							continue
						}
						userSamples = append(userSamples, userSampleRow{
							hub:      hub,
							name:     name,
							send:     sBytes,
							recv:     rBytes,
							numLogin: integer(u["NumLogin_u32"]),
						})
					}
				}()
			}
			if len(hubSamples) == 0 && len(userSamples) == 0 {
				return
			}
			tx, err := a.db.Begin()
			if err != nil {
				log.Printf("traffic sample begin tx %d: %v", serverID, err)
				return
			}
			defer tx.Rollback()
			if len(hubSamples) > 0 {
				hubStmt, err := tx.Prepare(`INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,NumSessions,NumUsers,SampledDate) VALUES(?,?,?,?,?,?,?)`)
				check(err)
				defer hubStmt.Close()
				for _, hs := range hubSamples {
					_, err = hubStmt.Exec(serverID, hs.hub, hs.send, hs.recv, hs.sessions, hs.users, stamp)
					check(err)
				}
			}
			if len(userSamples) > 0 {
				userStmt, err := tx.Prepare(`INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,NumLogin,SampledDate) VALUES(?,?,?,?,?,?,?)`)
				check(err)
				defer userStmt.Close()
				for _, us := range userSamples {
					_, err = userStmt.Exec(serverID, us.hub, us.name, us.send, us.recv, us.numLogin, stamp)
					check(err)
				}
			}
			check(tx.Commit())
		}()
	}
}
func (a *App) sampleSessions(ctx context.Context) {
	if !boolean(a.setting("session_monitor_enabled")) {
		return
	}
	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	for _, s := range servers {
		serverID := integer(s["ServerID"])
		serverCtx := context.WithValue(ctx, serverIDKey, serverID)
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("session sample server %d: %v", serverID, p)
				}
			}()
			for _, h := range list(a.rpc(serverCtx, "EnumHub", nil)["HubList"]) {
				hub := str(h["HubName_str"])
				if hub != "" {
					a.sampleHubSessions(serverCtx, serverID, hub)
				}
			}
		}()
	}
}
type liveSessionData struct {
	key        string
	username   string
	clientIP   string
	virtualIP  string
	clientHost string
	started    string
	packetSize int64
	packetNum  int64
	down       int64
	up         int64
	sampled    bool
}

func (a *App) sampleHubSessions(ctx context.Context, serverID int64, hub string) {
	rawLive := list(a.rpc(ctx, "EnumSession", M{"HubName_str": hub})["SessionList"])
	live := make([]M, 0, len(rawLive))
	for _, s := range rawLive {
		if isClientSessionRPC(s) {
			live = append(live, s)
		}
	}
	seen := map[string]bool{}
	stamp := now()
	series := boolean(a.setting("session_traffic_enabled"))
	sessionIPs, ipToDhcpHost := a.collectSessionNetMaps(ctx, hub)

	type sessDetail struct {
		down, up   int64
		sampled    bool
		clientHost string
	}
	details := make(map[string]sessDetail)
	if series && len(live) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, s := range live {
			key := str(s["UniqueId_bin"])
			sessName := str(s["Name_str"])
			clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
			if key == "" || sessName == "" {
				continue
			}
			wg.Add(1)
			go func(k, sn, cIP string) {
				defer wg.Done()
				defer func() {
					if p := recover(); p != nil {
						log.Printf("session traffic %d/%s/%s: %v", serverID, hub, k, p)
					}
				}()
				sem <- struct{}{}
				defer func() { <-sem }()
				status := a.rpc(ctx, "GetSessionStatus", M{"HubName_str": hub, "Name_str": sn})
				down, up := bytesFor(status, "")
				var chHost string
				cH := strings.TrimSpace(str(status["ClientHostname_str"]))
				sN := strings.TrimSpace(str(status["ServerName_str"]))
				if cH != "" && !isIP(cH) && cH != cIP && !strings.EqualFold(cH, sN) {
					chHost = cH
				}
				mu.Lock()
				details[k] = sessDetail{down: down, up: up, sampled: true, clientHost: chHost}
				mu.Unlock()
			}(key, sessName, clientIP)
		}
		wg.Wait()
	}

	toWrite := make([]liveSessionData, 0, len(live))
	for _, s := range live {
		key := str(s["UniqueId_bin"])
		if key == "" {
			continue
		}
		seen[key] = true
		sessName := str(s["Name_str"])
		clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
		remoteHost := strings.TrimSpace(str(s["RemoteHostname_str"]))

		var virtualIP string
		if ips, ok := sessionIPs[sessName]; ok && len(ips) > 0 {
			virtualIP = strings.Join(ips, ", ")
		}

		d := details[key]
		clientHost := d.clientHost
		if clientHost == "" {
			clientHost = firstDHCPHost(sessionIPs[sessName], ipToDhcpHost, remoteHost)
		}
		if clientHost == "" {
			h := strings.TrimSpace(str(s["Hostname_str"]))
			if h != "" && h != clientIP && !isIP(h) && !strings.EqualFold(h, remoteHost) {
				clientHost = h
			}
		}

		started := str(s["CreatedTime_dt"])
		if started == "" {
			started = stamp
		}
		toWrite = append(toWrite, liveSessionData{
			key:        key,
			username:   str(s["Username_str"]),
			clientIP:   clientIP,
			virtualIP:  virtualIP,
			clientHost: clientHost,
			started:    started,
			packetSize: integer(s["PacketSize_u64"]),
			packetNum:  integer(s["PacketNum_u64"]),
			down:       d.down,
			up:         d.up,
			sampled:    d.sampled,
		})
	}

	tx, err := a.db.Begin()
	check(err)
	defer tx.Rollback()

	insertSample, err := tx.Prepare(`INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,VirtualIP,ClientHostname,StartedDate,LastSeenDate,BytesTotal,PacketsTotal,DownloadBytes,UploadBytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,HubName,SessionKey) DO UPDATE SET LastSeenDate=excluded.LastSeenDate,BytesTotal=excluded.BytesTotal,PacketsTotal=excluded.PacketsTotal,VirtualIP=CASE WHEN excluded.VirtualIP != '' THEN excluded.VirtualIP ELSE VpnSessionSample.VirtualIP END,ClientHostname=CASE WHEN excluded.ClientHostname != '' THEN excluded.ClientHostname ELSE VpnSessionSample.ClientHostname END,DownloadBytes=CASE WHEN ? THEN excluded.DownloadBytes ELSE VpnSessionSample.DownloadBytes END,UploadBytes=CASE WHEN ? THEN excluded.UploadBytes ELSE VpnSessionSample.UploadBytes END`)
	check(err)
	defer insertSample.Close()

	for _, d := range toWrite {
		_, err = insertSample.Exec(serverID, hub, d.username, d.key, d.clientIP, d.virtualIP, d.clientHost, d.started, stamp, d.packetSize, d.packetNum, d.down, d.up, d.sampled, d.sampled)
		check(err)
		if d.sampled {
			var sampleID int64
			if err = tx.QueryRow(`SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND SessionKey=?`, serverID, hub, d.key).Scan(&sampleID); err == nil {
				_, err = tx.Exec(`INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate) VALUES(?,?,?,?,?)`, sampleID, d.down, d.up, d.packetSize, stamp)
				check(err)
			}
		}
	}

	rows, err := tx.Query(`SELECT SessionKey FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND EndedDate IS NULL AND `+clientSessionSQLClause, serverID, hub)
	check(err)
	var endedKeys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err == nil && !seen[k] {
			endedKeys = append(endedKeys, k)
		}
	}
	_ = rows.Close()

	if len(endedKeys) > 0 {
		updateEnded, err := tx.Prepare(`UPDATE VpnSessionSample SET EndedDate=LastSeenDate WHERE ServerID=? AND HubName=? AND SessionKey=? AND EndedDate IS NULL`)
		check(err)
		defer updateEnded.Close()
		for _, k := range endedKeys {
			_, err = updateEnded.Exec(serverID, hub, k)
			check(err)
		}
	}

	check(tx.Commit())
}
