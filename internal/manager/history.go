package manager

import (
	"context"
	"database/sql"
	"encoding/base64"
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
func (a *App) enrichSessionRowsGeo(ctx context.Context, rows []M, offlineOnly ...bool) {
	if len(rows) == 0 {
		return
	}
	ips := make([]string, 0, len(rows))
	for _, row := range rows {
		if str(row["ip_location"]) == "" {
			if ip := str(row["client_ip"]); ip != "" {
				ips = append(ips, ip)
			}
		}
	}
	if len(ips) == 0 {
		return
	}
	geoMap := a.batchLookupIPs(ctx, ips, offlineOnly...)
	for _, row := range rows {
		if str(row["ip_location"]) != "" {
			continue
		}
		if geo, ok := geoMap[cleanIP(str(row["client_ip"]))]; ok {
			row["ip_location"] = geo.Location
			row["ip_isp"] = geo.ISP
			row["ip_full"] = geo.Full
		}
	}
}

type userMetaEntry struct {
	realName string
	note     string
}

type hubUserMetaCache struct {
	fetchedAt time.Time
	users     map[string]userMetaEntry
}

const userMetaTTL = 2 * time.Minute

func userMetaCacheKey(serverID int64, hub string) string {
	return fmt.Sprintf("%d\x00%s", serverID, strings.ToLower(strings.TrimSpace(hub)))
}

func (a *App) updateUserMetaCache(serverID int64, hub string, users []M) map[string]userMetaEntry {
	entries := make(map[string]userMetaEntry, len(users))
	for _, u := range users {
		name := strings.ToLower(strings.TrimSpace(str(u["Name_str"])))
		if name != "" {
			entries[name] = userMetaEntry{
				realName: str(u["Realname_utf"]),
				note:     str(u["Note_utf"]),
			}
		}
	}
	a.userMetaMu.Lock()
	if a.userMeta == nil {
		a.userMeta = make(map[string]hubUserMetaCache)
	}
	a.userMeta[userMetaCacheKey(serverID, hub)] = hubUserMetaCache{
		fetchedAt: time.Now(),
		users:     entries,
	}
	a.userMetaMu.Unlock()
	return entries
}

func (a *App) invalidateUserMetaCache(serverID int64, hub string) {
	a.userMetaMu.Lock()
	defer a.userMetaMu.Unlock()
	if a.userMeta == nil {
		return
	}
	if strings.TrimSpace(hub) != "" {
		delete(a.userMeta, userMetaCacheKey(serverID, hub))
		return
	}
	prefix := fmt.Sprintf("%d\x00", serverID)
	for k := range a.userMeta {
		if strings.HasPrefix(k, prefix) {
			delete(a.userMeta, k)
		}
	}
}

func (a *App) enrichSessionRowsUser(ctx context.Context, rows []M) {
	if len(rows) == 0 {
		return
	}
	serverID := a.serverIDFromContext(ctx)
	hubSet := make(map[string]struct{})
	for _, row := range rows {
		if h := str(row["hub"]); h != "" {
			hubSet[h] = struct{}{}
		}
	}
	hubUsers := make(map[string]map[string]userMetaEntry, len(hubSet))
	for hub := range hubSet {
		key := userMetaCacheKey(serverID, hub)
		a.userMetaMu.RLock()
		cached, ok := a.userMeta[key]
		a.userMetaMu.RUnlock()
		if ok && time.Since(cached.fetchedAt) < userMetaTTL {
			hubUsers[strings.ToLower(hub)] = cached.users
			continue
		}
		var fetched []M
		rpcOK := false
		func() {
			defer func() { _ = recover() }()
			out := a.rpc(ctx, "EnumUser", M{"HubName_str": hub})
			fetched = list(out["UserList"])
			rpcOK = true
		}()
		if rpcOK {
			hubUsers[strings.ToLower(hub)] = a.updateUserMetaCache(serverID, hub, fetched)
		} else if ok {
			// Fall back to stale cached profile metadata if SoftEther RPC is temporarily unreachable.
			hubUsers[strings.ToLower(hub)] = cached.users
		}
	}
	for _, row := range rows {
		hub := strings.ToLower(str(row["hub"]))
		username := strings.ToLower(strings.TrimSpace(str(row["username"])))
		if entry, ok := hubUsers[hub][username]; ok {
			row["real_name"] = entry.realName
			row["note"] = entry.note
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

const clientSessionSQLClause = `LOWER(UserName) NOT IN ('securenat', 'local bridge') AND LOWER(UserName) NOT GLOB 'sid-local-bridge*' AND LOWER(UserName) NOT GLOB 'link_*' AND LOWER(UserName) NOT GLOB 'l3_*'`
const nonClientSessionDeleteClause = `LOWER(UserName) IN ('securenat', 'local bridge') OR LOWER(UserName) GLOB 'sid-local-bridge*' OR LOWER(UserName) GLOB 'link_*' OR LOWER(UserName) GLOB 'l3_*'`

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
	if proto := strings.TrimSpace(q.Get("protocol")); proto != "" {
		if strings.EqualFold(proto, "unknown") {
			clauses = append(clauses, "Protocol=''")
		} else {
			clauses = append(clauses, "Protocol=? COLLATE NOCASE")
			args = append(args, proto)
		}
	}
	if search := strings.TrimSpace(q.Get("q")); search != "" {
		pat := "%" + escapeLike(search) + "%"
		clauses = append(clauses, `(UserName LIKE ? ESCAPE '\' OR RealName LIKE ? ESCAPE '\' OR Note LIKE ? ESCAPE '\' OR ClientIP LIKE ? ESCAPE '\' OR IpLocation LIKE ? ESCAPE '\' OR VirtualIP LIKE ? ESCAPE '\' OR ClientHostname LIKE ? ESCAPE '\' OR HubName LIKE ? ESCAPE '\')`)
		args = append(args, pat, pat, pat, pat, pat, pat, pat, pat)
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
	a.endpoint("GET /server/analytics", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		hours := queryInt(r, "hours", 24, 1, 2160)
		hub := strings.TrimSpace(r.URL.Query().Get("hub"))
		return a.serverAnalytics(a.serverIDFromContext(r.Context()), hub, hours)
	})
	a.endpoint("GET /server/session-history/analytics", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		return a.sessionReportAnalytics(a.serverIDFromContext(r.Context()), r)
	})
	a.endpoint("GET /server/session-history", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		where, args := buildSessionReportFilter(serverID, r)
		limit := queryInt(r, "limit", 100, 1, 500)
		offset := queryInt(r, "offset", 0, 0, 1000000)

		orderBy := `julianday(StartedDate) DESC, VpnSessionSampleID DESC`
		switch strings.TrimSpace(r.URL.Query().Get("sort")) {
		case "", "started":
		case "bytes":
			orderBy = `BytesTotal DESC, VpnSessionSampleID DESC`
		case "duration":
			orderBy = `(COALESCE(EndedTs, unixepoch(COALESCE(EndedDate, LastSeenDate))) - COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate))) DESC, VpnSessionSampleID DESC`
		default:
			fail(422, "Invalid sort parameter.")
		}

		rdb := a.readDB()
		summary := one(rdb, `SELECT COUNT(*) total_sessions, COALESCE(SUM(CASE WHEN EndedDate IS NULL THEN 1 ELSE 0 END), 0) active_sessions, COUNT(DISTINCT LOWER(UserName)) unique_users, COALESCE(SUM(BytesTotal), 0) total_bytes, COALESCE(SUM(DownloadBytes), 0) download_bytes, COALESCE(SUM(UploadBytes), 0) upload_bytes FROM VpnSessionSample WHERE `+where, args...)
		if summary == nil {
			summary = M{"total_sessions": 0, "active_sessions": 0, "unique_users": 0, "total_bytes": 0, "download_bytes": 0, "upload_bytes": 0}
		}

		hubRows := query(rdb, `SELECT DISTINCT HubName hub FROM VpnSessionSample WHERE ServerID=? AND `+clientSessionSQLClause+` ORDER BY HubName`, serverID)
		hubs := make([]string, 0, len(hubRows))
		for _, hr := range hubRows {
			if h := str(hr["hub"]); h != "" {
				hubs = append(hubs, h)
			}
		}

		itemArgs := append(append([]any{}, args...), limit, offset)
		rows := query(rdb, `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes,RealName real_name,Note note,IpLocation ip_location,IpISP ip_isp,IpFull ip_full,Protocol protocol,DisconnectReason disconnect_reason,ServerPort server_port,ClientPort client_port FROM VpnSessionSample WHERE `+where+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, itemArgs...)
		a.enrichSessionRowsGeo(r.Context(), rows)
		a.enrichSessionRowsUser(r.Context(), rows)
		return M{"items": rows, "total": integer(summary["total_sessions"]), "summary": summary, "hubs": hubs}
	})
	a.endpoint("GET /server/session-history/export", func(w http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		where, args := buildSessionReportFilter(serverID, r)
		loc := resolveExportLocation(r)
		zh := strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("lang"))), "zh")

		rows := query(a.readDB(), `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes,RealName real_name,Note note,IpLocation ip_location,IpISP ip_isp,IpFull ip_full,Protocol protocol,DisconnectReason disconnect_reason,ServerPort server_port,ClientPort client_port FROM VpnSessionSample WHERE `+where+` ORDER BY julianday(StartedDate) DESC, VpnSessionSampleID DESC LIMIT 50000`, args...)
		a.enrichSessionRowsGeo(r.Context(), rows, len(rows) > 200)
		a.enrichSessionRowsUser(r.Context(), rows)

		filename := fmt.Sprintf("session-audit-report-%s.csv", time.Now().In(loc).Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("\xef\xbb\xbf"))

		cw := csv.NewWriter(w)
		headers := []string{"Session ID", "User", "Virtual Hub", "Status", "Started", "Ended", "Duration (s)", "Duration", "Client IP", "IP Location", "ISP", "Virtual IP", "Hostname", "Download (Bytes)", "Upload (Bytes)", "Total (Bytes)", "Packets", "Protocol", "Disconnect Reason"}
		activeText, endedText := "Active", "Ended"
		if zh {
			headers = []string{"会话 ID", "用户名", "虚拟 HUB", "状态", "上线时间", "下线时间", "在线时长(秒)", "在线时长", "客户端 IP", "IP 属地", "运营商", "虚拟 IP", "主机名", "下载流量(Bytes)", "上传流量(Bytes)", "总流量(Bytes)", "数据包", "协议", "断开原因"}
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
				safeCSVCell(str(row["protocol"])),
				safeCSVCell(str(row["disconnect_reason"])),
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
			statement := `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes,RealName real_name,Note note,IpLocation ip_location,IpISP ip_isp,IpFull ip_full,Protocol protocol,DisconnectReason disconnect_reason,ServerPort server_port,ClientPort client_port FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND ` + clientSessionSQLClause
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
			rows := query(a.readDB(), statement, args...)
			a.enrichSessionRowsGeo(r.Context(), rows)
			a.enrichSessionRowsUser(r.Context(), rows)
			return rows
		})
	}
	a.endpoint("GET /hubs/{hub}/session-history/{session_id}/usage", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		rdb := a.readDB()
		session := one(rdb, `SELECT VpnSessionSampleID id,UserName username,ClientIP client_ip,VirtualIP virtual_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,DownloadBytes download_bytes,UploadBytes upload_bytes,RealName real_name,Note note,IpLocation ip_location,IpISP ip_isp,IpFull ip_full,Protocol protocol,DisconnectReason disconnect_reason,ServerPort server_port,ClientPort client_port FROM VpnSessionSample WHERE ServerID=? AND VpnSessionSampleID=? AND HubName=? AND `+clientSessionSQLClause, serverID, r.PathValue("session_id"), r.PathValue("hub"))
		if session == nil {
			fail(404, "No such session in this hub's history.")
		}
		if str(session["ip_location"]) == "" {
			geo := a.lookupIP(r.Context(), str(session["client_ip"]))
			session["ip_location"] = geo.Location
			session["ip_isp"] = geo.ISP
			session["ip_full"] = geo.Full
		}
		rows := query(rdb, `SELECT DownloadBytes,UploadBytes,TotalBytes,SampledDate FROM VpnSessionTrafficSample WHERE VpnSessionSampleID=? ORDER BY COALESCE(NULLIF(SampledTs,0), unixepoch(SampledDate)), VpnSessionTrafficSampleID`, session["id"])
		end := time.Now()
		if s := str(session["ended_date"]); s != "" {
			end = parseTime(s)
		}
		startedAt := parseTime(str(session["started_date"]))
		sessionInterval := max(int64(5), integer(a.setting("session_interval_seconds")))
		if len(rows) > 0 && !startedAt.IsZero() {
			if firstAt := parseTime(str(rows[0]["SampledDate"])); !firstAt.IsZero() && firstAt.After(startedAt) && firstAt.Sub(startedAt) <= max(2*time.Duration(sessionInterval)*time.Second, 2*time.Minute) {
				rows = append([]M{{
					"DownloadBytes": int64(0),
					"UploadBytes":   int64(0),
					"TotalBytes":    int64(0),
					"SampledDate":   startedAt.UTC().Format(time.RFC3339Nano),
				}}, rows...)
			}
		}
		hours := max(int64(1), int64(end.Sub(startedAt).Hours()+0.5))
		out := usage(rows, hours, "UploadBytes", "DownloadBytes", false)
		out["start"] = startedAt.UTC().Format(time.RFC3339Nano)
		out["end"] = end.UTC().Format(time.RFC3339Nano)
		out["sample_interval_seconds"] = sessionInterval
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
				started := time.Now()
				func() {
					defer func() {
						if p := recover(); p != nil {
							log.Printf("background task: %v", p)
						}
					}()
					fn(ctx)
					delay = max(time.Second, interval()-time.Since(started))
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
func (a *App) maybePrune() {
	a.pruneMu.Lock()
	if !a.lastPruneAt.IsZero() && time.Since(a.lastPruneAt) < 30*time.Minute {
		a.pruneMu.Unlock()
		return
	}
	a.lastPruneAt = time.Now()
	a.pruneMu.Unlock()
	a.prune()
}

func (a *App) prune() {
	// Purge any legacy non-client session records (e.g. SecureNAT, Local Bridge, Cascade, L3 switch)
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE VpnSessionSampleID IN (SELECT VpnSessionSampleID FROM VpnSessionSample WHERE `+nonClientSessionDeleteClause+`)`)
	execute(a.db, `DELETE FROM VpnSessionSample WHERE `+nonClientSessionDeleteClause)

	days := max(int64(1), integer(a.setting("sample_retention_days")))
	cutoffTs := time.Now().UTC().AddDate(0, 0, -int(days)).Unix()
	cutoffArg := fmt.Sprintf("-%d days", days)
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample"} {
		execute(a.db, `DELETE FROM `+table+` WHERE (SampledTs>0 AND SampledTs<?) OR (SampledTs=0 AND datetime(SampledDate)<datetime('now',?))`, cutoffTs, cutoffArg)
	}
	rollupDays := max(int64(365), days)
	rollupCutoffTs := time.Now().UTC().AddDate(0, 0, -int(rollupDays)).Unix()
	execute(a.db, `DELETE FROM TrafficRollupHourly WHERE BucketTs<?`, rollupCutoffTs)

	days = max(int64(1), integer(a.setting("session_history_retention_days")))
	sessCutoffTs := time.Now().UTC().AddDate(0, 0, -int(days)).Unix()
	sessCutoffArg := fmt.Sprintf("-%d days", days)
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE (SampledTs>0 AND SampledTs<?) OR (SampledTs=0 AND datetime(SampledDate)<datetime('now',?))`, sessCutoffTs, sessCutoffArg)
	execute(a.db, `DELETE FROM VpnSessionTrafficSample WHERE VpnSessionSampleID IN (SELECT VpnSessionSampleID FROM VpnSessionSample WHERE (COALESCE(EndedTs,0)>0 AND EndedTs<?) OR (COALESCE(EndedTs,0)=0 AND datetime(COALESCE(EndedDate,LastSeenDate))<datetime('now',?)))`, sessCutoffTs, sessCutoffArg)
	execute(a.db, `DELETE FROM VpnSessionSample WHERE (COALESCE(EndedTs,0)>0 AND EndedTs<?) OR (COALESCE(EndedTs,0)=0 AND datetime(COALESCE(EndedDate,LastSeenDate))<datetime('now',?))`, sessCutoffTs, sessCutoffArg)
}

type trafficCursorState struct {
	lastSend        int64
	lastRecv        int64
	serverStartTime string
	lastSampledTs   int64
}

func computeTrafficSampleDelta(prev trafficCursorState, hasPrev bool, send, recv, stampTs int64, serverStartTime string) (deltaSend, deltaRecv int64, hasDelta bool, nextCursor trafficCursorState, updateCursor bool) {
	nextCursor = trafficCursorState{
		lastSend:        send,
		lastRecv:        recv,
		serverStartTime: serverStartTime,
		lastSampledTs:   stampTs,
	}
	if !hasPrev {
		// Baseline observation: never invent historical lifetime counters as interval traffic.
		if send == 0 && recv == 0 {
			return 0, 0, true, nextCursor, true
		}
		return 0, 0, false, nextCursor, true
	}
	elapsed := max(int64(1), stampTs-prev.lastSampledTs)
	if send >= prev.lastSend && recv >= prev.lastRecv {
		if prev.lastSend == 0 && prev.lastRecv == 0 && (send > 0 || recv > 0) && send+recv > maxSingleIntervalBytesPerSec*elapsed {
			return 0, 0, false, nextCursor, false
		}
		return send - prev.lastSend, recv - prev.lastRecv, true, nextCursor, true
	}
	// Counter dropped (e.g. SoftEther service restart or transient zero read).
	if send == 0 && recv == 0 {
		if serverStartTime != "" && prev.serverStartTime != "" && serverStartTime == prev.serverStartTime {
			// Server did not restart: ignore suspicious zero glitch without clobbering cursor.
			return 0, 0, false, prev, false
		}
		return 0, 0, true, nextCursor, true
	}
	restarted := serverStartTime != "" && prev.serverStartTime != "" && serverStartTime != prev.serverStartTime
	if restarted || send+recv <= maxSingleIntervalBytesPerSec*elapsed {
		return send, recv, true, nextCursor, true
	}
	return 0, 0, false, prev, false
}

func (a *App) sampleTraffic(ctx context.Context) {
	a.maybePrune()
	if !boolean(a.setting("traffic_monitor_enabled")) {
		return
	}
	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	if len(servers) == 0 {
		return
	}
	stampTime := time.Now().UTC().Truncate(time.Second)
	stamp := stampTime.Format("2006-01-02T15:04:05+00:00")
	stampTs := stampTime.Unix()
	bucketTs := stampTs - (stampTs % 3600)

	var wg sync.WaitGroup
	for _, s := range servers {
		serverID := integer(s["ServerID"])
		wg.Add(1)
		go func(sid int64) {
			defer wg.Done()
			serverCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			serverCtx = context.WithValue(serverCtx, serverIDKey, sid)
			defer func() {
				if p := recover(); p != nil {
					log.Printf("traffic sample server %d: %v", sid, p)
				}
			}()

			type hubSampleRow struct {
				hub             string
				send, recv      int64
				sessions, users int64
			}
			type userSampleRow struct {
				hub, name  string
				send, recv int64
				numLogin   int64
			}

			var serverStartTime string
			func() {
				defer func() { _ = recover() }()
				st := a.rpc(serverCtx, "GetServerStatus", nil)
				serverStartTime = strings.TrimSpace(str(st["StartTime_dt"]))
			}()

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
							log.Printf("traffic sample %d/%s: %v", sid, hub, p)
						}
					}()
					var status M
					var users []M
					var hubWG sync.WaitGroup
					hubWG.Add(2)
					go func() {
						defer hubWG.Done()
						defer func() { _ = recover() }()
						status = a.rpc(serverCtx, "GetHubStatus", M{"HubName_str": hub})
					}()
					go func() {
						defer hubWG.Done()
						defer func() { _ = recover() }()
						users = list(a.rpc(serverCtx, "EnumUser", M{"HubName_str": hub})["UserList"])
					}()
					hubWG.Wait()

					if status != nil {
						if send, recv, valid := sampledTraffic(status, ""); valid {
							hubSamples = append(hubSamples, hubSampleRow{
								hub:      hub,
								send:     send,
								recv:     recv,
								sessions: integer(status["NumSessions_u32"]),
								users:    integer(status["NumUsers_u32"]),
							})
						} else {
							log.Printf("traffic sample %d/%s: missing or invalid hub counters", sid, hub)
						}
					}
					if len(users) > 0 {
						a.updateUserMetaCache(sid, hub, users)
						for _, u := range users {
							name := str(u["Name_str"])
							sBytes, rBytes, valid := userTrafficCounters(u)
							if !valid {
								log.Printf("traffic sample %d/%s/%s: missing or invalid user counters", sid, hub, name)
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
					}
				}()
			}
			if len(hubSamples) == 0 && len(userSamples) == 0 {
				return
			}

			tx, err := a.db.Begin()
			if err != nil {
				log.Printf("traffic sample begin tx %d: %v", sid, err)
				return
			}
			defer tx.Rollback()

			cursors := map[string]trafficCursorState{}
			cRows, err := tx.Query(`SELECT SubjectType,HubName,UserKey,LastSendBytes,LastRecvBytes,ServerStartTime,LastSampledTs FROM TrafficCursor WHERE ServerID=?`, sid)
			if err == nil {
				for cRows.Next() {
					var subj, hubName, uKey, sStart string
					var lSend, lRecv, lTs int64
					if scanErr := cRows.Scan(&subj, &hubName, &uKey, &lSend, &lRecv, &sStart, &lTs); scanErr == nil {
						cursors[subj+"\x00"+hubName+"\x00"+uKey] = trafficCursorState{
							lastSend:        lSend,
							lastRecv:        lRecv,
							serverStartTime: sStart,
							lastSampledTs:   lTs,
						}
					}
				}
				_ = cRows.Close()
			}

			cursorStmt, err := tx.Prepare(`INSERT INTO TrafficCursor(ServerID,SubjectType,HubName,UserKey,LastSendBytes,LastRecvBytes,ServerStartTime,LastSampledTs) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,SubjectType,HubName,UserKey) DO UPDATE SET LastSendBytes=excluded.LastSendBytes,LastRecvBytes=excluded.LastRecvBytes,ServerStartTime=excluded.ServerStartTime,LastSampledTs=excluded.LastSampledTs`)
			check(err)
			defer cursorStmt.Close()

			rollupStmt, err := tx.Prepare(`INSERT INTO TrafficRollupHourly(ServerID,SubjectType,HubName,UserKey,UserName,BucketTs,SendBytes,RecvBytes,MaxSessions,SampleCount) VALUES(?,?,?,?,?,?,?,?,?,1) ON CONFLICT(ServerID,SubjectType,HubName,UserKey,BucketTs) DO UPDATE SET UserName=CASE WHEN excluded.UserName!='' THEN excluded.UserName ELSE TrafficRollupHourly.UserName END,SendBytes=TrafficRollupHourly.SendBytes+excluded.SendBytes,RecvBytes=TrafficRollupHourly.RecvBytes+excluded.RecvBytes,MaxSessions=MAX(TrafficRollupHourly.MaxSessions,excluded.MaxSessions),SampleCount=TrafficRollupHourly.SampleCount+1`)
			check(err)
			defer rollupStmt.Close()

			if len(hubSamples) > 0 {
				hubStmt, err := tx.Prepare(`INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,NumSessions,NumUsers,SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
				check(err)
				defer hubStmt.Close()
				for _, hs := range hubSamples {
					cKey := "hub\x00" + hs.hub + "\x00"
					prev, hasPrev := cursors[cKey]
					dSend, dRecv, hasDelta, nextC, upd := computeTrafficSampleDelta(prev, hasPrev, hs.send, hs.recv, stampTs, serverStartTime)
					hasDeltaInt := 0
					if hasDelta {
						hasDeltaInt = 1
					}
					_, err = hubStmt.Exec(sid, hs.hub, hs.send, hs.recv, hs.sessions, hs.users, stamp, stampTs, dSend, dRecv, hasDeltaInt)
					check(err)
					if upd {
						cursors[cKey] = nextC
						_, err = cursorStmt.Exec(sid, "hub", hs.hub, "", nextC.lastSend, nextC.lastRecv, nextC.serverStartTime, nextC.lastSampledTs)
						check(err)
					}
					if hasDelta {
						_, err = rollupStmt.Exec(sid, "hub", hs.hub, "", "", bucketTs, dSend, dRecv, hs.sessions)
						check(err)
					}
				}
			}
			if len(userSamples) > 0 {
				userStmt, err := tx.Prepare(`INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,NumLogin,SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta) VALUES(?,?,?,?,?,?,?,?,?,?,?)`)
				check(err)
				defer userStmt.Close()
				for _, us := range userSamples {
					uKey := fold(us.name)
					cKey := "user\x00" + us.hub + "\x00" + uKey
					prev, hasPrev := cursors[cKey]
					dSend, dRecv, hasDelta, nextC, upd := computeTrafficSampleDelta(prev, hasPrev, us.send, us.recv, stampTs, serverStartTime)
					hasDeltaInt := 0
					if hasDelta {
						hasDeltaInt = 1
					}
					_, err = userStmt.Exec(sid, us.hub, us.name, us.send, us.recv, us.numLogin, stamp, stampTs, dSend, dRecv, hasDeltaInt)
					check(err)
					if upd {
						cursors[cKey] = nextC
						_, err = cursorStmt.Exec(sid, "user", us.hub, uKey, nextC.lastSend, nextC.lastRecv, nextC.serverStartTime, nextC.lastSampledTs)
						check(err)
					}
					if hasDelta {
						_, err = rollupStmt.Exec(sid, "user", us.hub, uKey, us.name, bucketTs, dSend, dRecv, 0)
						check(err)
					}
				}
			}
			check(tx.Commit())
		}(serverID)
	}
	wg.Wait()
}
func userTrafficCounters(u M) (int64, int64, bool) {
	if send, recv, valid := sampledTraffic(u, "Ex."); valid {
		return send, recv, true
	}
	if _, hasExSend := u["Ex.Send.UnicastBytes_u64"]; !hasExSend {
		if _, hasPlainSend := u["Send.UnicastBytes_u64"]; hasPlainSend {
			return sampledTraffic(u, "")
		}
	}
	return 0, 0, false
}

func (a *App) closeHubOpenSessions(serverID int64, hub string) {
	execute(a.db, `UPDATE VpnSessionSample SET EndedDate=LastSeenDate,EndedTs=COALESCE(NULLIF(EndedTs,0), unixepoch(LastSeenDate)) WHERE ServerID=? AND HubName=? AND EndedDate IS NULL AND `+clientSessionSQLClause, serverID, hub)
}

func (a *App) sampleSessions(ctx context.Context) {
	if !boolean(a.setting("session_monitor_enabled")) {
		return
	}
	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	var wg sync.WaitGroup
	for _, s := range servers {
		serverID := integer(s["ServerID"])
		wg.Add(1)
		go func(sid int64) {
			defer wg.Done()
			serverCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			serverCtx = context.WithValue(serverCtx, serverIDKey, sid)
			defer func() {
				if p := recover(); p != nil {
					log.Printf("session sample server %d: %v", sid, p)
				}
			}()
			for _, h := range list(a.rpc(serverCtx, "EnumHub", nil)["HubList"]) {
				hub := str(h["HubName_str"])
				if hub == "" {
					continue
				}
				func() {
					defer func() {
						if p := recover(); p != nil {
							log.Printf("session sample %d/%s: %v", sid, hub, p)
						}
					}()
					if _, hasOnline := h["Online_bool"]; hasOnline && !boolean(h["Online_bool"]) {
						a.closeHubOpenSessions(sid, hub)
						return
					}
					a.sampleHubSessions(serverCtx, sid, hub)
				}()
			}
			a.syncSessionLogs(serverCtx, sid)
		}(serverID)
	}
	wg.Wait()
}

type userLoginObservation struct {
	numLogin      int64
	lastLogin     time.Time
	send, recv    int64
	validCounters bool
	seenAt        time.Time
}

type liveSessionData struct {
	key        string
	sessName   string
	username   string
	clientIP   string
	virtualIP  string
	clientHost string
	started    string
	startedTs  int64
	packetSize int64
	packetNum  int64
	down       int64
	up         int64
	sampled    bool
	realName   string
	note       string
	ipLocation string
	ipISP      string
	ipFull     string
	protocol   string
	serverPort int64
	clientPort int64
}

func validSessionUID(raw string) bool {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Non-base64 test keys such as "stable-key" remain valid identifiers.
		return true
	}
	for _, b := range decoded {
		if b != 0 {
			return true
		}
	}
	return false
}

func fallbackSessionKey(sessName, username, started string) string {
	if sessName != "" {
		return "sess:" + sessName + "@" + started
	}
	return "user:" + strings.ToLower(strings.TrimSpace(username)) + "@" + started
}

func sameTrackedSession(existingUser, existingStarted string, existingEnded sql.NullString, d liveSessionData) bool {
	if existingEnded.Valid && strings.TrimSpace(existingEnded.String) != "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(existingUser), strings.TrimSpace(d.username)) {
		return false
	}
	prevAt := parseTime(existingStarted)
	currAt := parseTime(d.started)
	if !prevAt.IsZero() && !currAt.IsZero() {
		diff := currAt.Sub(prevAt)
		if diff < 0 {
			diff = -diff
		}
		if diff > 30*time.Second {
			return false
		}
	}
	return true
}

func resolveSessionKey(tx *sql.Tx, serverID int64, hub string, d liveSessionData, used map[string]bool) (string, error) {
	base := d.key
	suffix := d.sessName
	if suffix == "" {
		suffix = strings.ToLower(strings.TrimSpace(d.username))
	}
	alt := fmt.Sprintf("%s#%s@%s", base, suffix, d.started)
	for attempt := 0; attempt < 64; attempt++ {
		candidate := base
		if attempt == 1 {
			candidate = alt
		} else if attempt > 1 {
			candidate = fmt.Sprintf("%s#%d", alt, attempt)
		}
		if used[candidate] {
			continue
		}
		var sampleID int64
		var existingUser, existingStarted string
		var existingEnded sql.NullString
		err := tx.QueryRow(`SELECT VpnSessionSampleID,UserName,StartedDate,EndedDate FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND SessionKey=?`, serverID, hub, candidate).Scan(&sampleID, &existingUser, &existingStarted, &existingEnded)
		if err == sql.ErrNoRows {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		if sameTrackedSession(existingUser, existingStarted, existingEnded, d) {
			return candidate, nil
		}
		// Another session previously held this SessionKey without being closed; close it before allocating a fresh key.
		if !existingEnded.Valid || strings.TrimSpace(existingEnded.String) == "" {
			if _, err := tx.Exec(`UPDATE VpnSessionSample SET EndedDate=LastSeenDate WHERE VpnSessionSampleID=? AND EndedDate IS NULL`, sampleID); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("%s#%s@%s#%d", base, suffix, d.started, time.Now().UnixNano()), nil
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
	usedKeys := map[string]bool{}
	stamp := now()
	stampTime := parseTime(stamp).UTC()
	if stampTime.IsZero() {
		stampTime = time.Now().UTC()
	}
	stampTs := stampTime.Unix()
	series := boolean(a.setting("session_traffic_enabled"))

	// Preload open sessions so unchanged idle sessions can skip redundant GetSessionStatus RPCs.
	type openSessState struct {
		bytesTotal int64
		down, up   int64
		clientHost string
		protocol   string
		serverPort int64
		clientPort int64
	}
	openByKey := map[string]openSessState{}
	for _, row := range query(a.readDB(), `SELECT SessionKey,BytesTotal,DownloadBytes,UploadBytes,ClientHostname,Protocol,ServerPort,ClientPort FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND EndedDate IS NULL AND `+clientSessionSQLClause, serverID, hub) {
		openByKey[str(row["SessionKey"])] = openSessState{
			bytesTotal: integer(row["BytesTotal"]),
			down:       integer(row["DownloadBytes"]),
			up:         integer(row["UploadBytes"]),
			clientHost: str(row["ClientHostname"]),
			protocol:   str(row["Protocol"]),
			serverPort: integer(row["ServerPort"]),
			clientPort: integer(row["ClientPort"]),
		}
	}

	var sessionIPs map[string][]string
	var ipToDhcpHost map[string]string
	var enumUsers []M
	var geoMap map[string]IPGeoInfo

	var prepWG sync.WaitGroup
	if len(live) > 0 {
		prepWG.Add(2)
		go func() {
			defer prepWG.Done()
			sessionIPs, ipToDhcpHost = a.collectSessionNetMaps(ctx, hub)
		}()
		go func() {
			defer prepWG.Done()
			ips := make([]string, 0, len(live))
			for _, s := range live {
				if cIP := str(s["ClientIP_ip"]); cIP != "" {
					ips = append(ips, cIP)
				}
			}
			geoMap = a.batchLookupIPs(ctx, ips, true)
		}()
	}
	prepWG.Add(1)
	go func() {
		defer prepWG.Done()
		defer func() { _ = recover() }()
		enumUsers = list(a.rpc(ctx, "EnumUser", M{"HubName_str": hub})["UserList"])
		a.updateUserMetaCache(serverID, hub, enumUsers)
	}()

	type sessDetail struct {
		down, up   int64
		sampled    bool
		clientHost string
		protocol   string
		serverPort int64
		clientPort int64
	}
	details := make(map[string]sessDetail)
	if series && len(live) > 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, s := range live {
			sessName := str(s["Name_str"])
			clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
			if sessName == "" {
				continue
			}
			username := str(s["Username_str"])
			started := str(s["CreatedTime_dt"])
			if startedAt := parseTime(started); started == "" || startedAt.IsZero() || startedAt.Year() < 1990 {
				started = stamp
			}
			key := strings.TrimSpace(str(s["UniqueId_bin"]))
			if !validSessionUID(key) {
				key = fallbackSessionKey(sessName, username, started)
			}
			packetSize := integer(s["PacketSize_u64"])
			if prev, ok := openByKey[key]; ok && packetSize == prev.bytesTotal && (prev.down+prev.up == packetSize || packetSize == 0) && (prev.down+prev.up > 0 || prev.clientHost != "" || prev.protocol != "" || prev.serverPort > 0 || prev.clientPort > 0) {
				// Traffic and identity are unchanged since last poll: reuse last known counters without an RPC.
				mu.Lock()
				details[sessName] = sessDetail{
					down:       prev.down,
					up:         prev.up,
					sampled:    true,
					clientHost: prev.clientHost,
					protocol:   prev.protocol,
					serverPort: prev.serverPort,
					clientPort: prev.clientPort,
				}
				mu.Unlock()
				continue
			}
			wg.Add(1)
			go func(sess M, sn, cIP string) {
				defer wg.Done()
				defer func() {
					if p := recover(); p != nil {
						log.Printf("session traffic %d/%s/%s: %v", serverID, hub, sn, p)
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
				sPort, cPort := extractSessionPorts(status)
				proto := detectSessionProtocol(sess, status)
				mu.Lock()
				details[sn] = sessDetail{
					down:       down,
					up:         up,
					sampled:    true,
					clientHost: chHost,
					protocol:   proto,
					serverPort: sPort,
					clientPort: cPort,
				}
				mu.Unlock()
			}(s, sessName, clientIP)
		}
		wg.Wait()
	}
	prepWG.Wait()

	userMetaByName := make(map[string]userMetaEntry, len(enumUsers))
	for _, u := range enumUsers {
		uName := strings.ToLower(strings.TrimSpace(str(u["Name_str"])))
		if uName != "" {
			userMetaByName[uName] = userMetaEntry{
				realName: str(u["Realname_utf"]),
				note:     str(u["Note_utf"]),
			}
		}
	}

	toWrite := make([]liveSessionData, 0, len(live))
	liveUserStarted := make(map[string][]time.Time)
	for _, s := range live {
		sessName := str(s["Name_str"])
		username := str(s["Username_str"])
		started := str(s["CreatedTime_dt"])
		startedAt := parseTime(started).UTC()
		if started == "" || startedAt.IsZero() || startedAt.Year() < 1990 {
			started = stamp
			startedAt = stampTime
		}
		key := strings.TrimSpace(str(s["UniqueId_bin"]))
		if !validSessionUID(key) {
			key = fallbackSessionKey(sessName, username, started)
		}
		if key == "" {
			continue
		}
		clientIP := strings.TrimSpace(str(s["ClientIP_ip"]))
		remoteHost := strings.TrimSpace(str(s["RemoteHostname_str"]))

		var virtualIP string
		if ips, ok := sessionIPs[sessName]; ok && len(ips) > 0 {
			virtualIP = strings.Join(ips, ", ")
		}

		d := details[sessName]
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

		uKey := strings.ToLower(strings.TrimSpace(username))
		liveUserStarted[uKey] = append(liveUserStarted[uKey], startedAt)

		proto := d.protocol
		if proto == "" {
			proto = detectSessionProtocol(s, nil)
		}
		geo := geoMap[cleanIP(clientIP)]
		meta := userMetaByName[uKey]

		toWrite = append(toWrite, liveSessionData{
			key:        key,
			sessName:   sessName,
			username:   username,
			clientIP:   clientIP,
			virtualIP:  virtualIP,
			clientHost: clientHost,
			started:    started,
			startedTs:  startedAt.Unix(),
			packetSize: integer(s["PacketSize_u64"]),
			packetNum:  integer(s["PacketNum_u64"]),
			down:       d.down,
			up:         d.up,
			sampled:    d.sampled,
			realName:   meta.realName,
			note:       meta.note,
			ipLocation: geo.Location,
			ipISP:      geo.ISP,
			ipFull:     geo.Full,
			protocol:   proto,
			serverPort: d.serverPort,
			clientPort: d.clientPort,
		})
	}

	sessionInterval := max(int64(5), integer(a.setting("session_interval_seconds")))

	tx, err := a.db.Begin()
	check(err)
	defer tx.Rollback()

	insertSample, err := tx.Prepare(`INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,VirtualIP,ClientHostname,StartedDate,LastSeenDate,BytesTotal,PacketsTotal,DownloadBytes,UploadBytes,StartedTs,RealName,Note,IpLocation,IpISP,IpFull,Protocol,ServerPort,ClientPort)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(ServerID,HubName,SessionKey) DO UPDATE SET
		  LastSeenDate=excluded.LastSeenDate,
		  BytesTotal=excluded.BytesTotal,
		  PacketsTotal=excluded.PacketsTotal,
		  StartedTs=CASE WHEN VpnSessionSample.StartedTs=0 THEN excluded.StartedTs ELSE VpnSessionSample.StartedTs END,
		  ClientIP=CASE WHEN excluded.ClientIP != '' THEN excluded.ClientIP ELSE VpnSessionSample.ClientIP END,
		  VirtualIP=CASE WHEN excluded.VirtualIP != '' THEN excluded.VirtualIP ELSE VpnSessionSample.VirtualIP END,
		  ClientHostname=CASE WHEN excluded.ClientHostname != '' THEN excluded.ClientHostname ELSE VpnSessionSample.ClientHostname END,
		  DownloadBytes=CASE WHEN ? THEN excluded.DownloadBytes ELSE VpnSessionSample.DownloadBytes END,
		  UploadBytes=CASE WHEN ? THEN excluded.UploadBytes ELSE VpnSessionSample.UploadBytes END,
		  RealName=CASE WHEN excluded.RealName != '' THEN excluded.RealName ELSE VpnSessionSample.RealName END,
		  Note=CASE WHEN excluded.Note != '' THEN excluded.Note ELSE VpnSessionSample.Note END,
		  IpLocation=CASE WHEN excluded.IpLocation != '' THEN excluded.IpLocation ELSE VpnSessionSample.IpLocation END,
		  IpISP=CASE WHEN excluded.IpISP != '' THEN excluded.IpISP ELSE VpnSessionSample.IpISP END,
		  IpFull=CASE WHEN excluded.IpFull != '' THEN excluded.IpFull ELSE VpnSessionSample.IpFull END,
		  Protocol=CASE WHEN excluded.Protocol != '' THEN excluded.Protocol ELSE VpnSessionSample.Protocol END,
		  ServerPort=CASE WHEN excluded.ServerPort > 0 THEN excluded.ServerPort ELSE VpnSessionSample.ServerPort END,
		  ClientPort=CASE WHEN excluded.ClientPort > 0 THEN excluded.ClientPort ELSE VpnSessionSample.ClientPort END
		RETURNING VpnSessionSampleID`)
	check(err)
	defer insertSample.Close()

	trafficStmt, err := tx.Prepare(`INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate,SampledTs) VALUES(?,?,?,?,?,?)`)
	check(err)
	defer trafficStmt.Close()

	for _, d := range toWrite {
		actualKey, err := resolveSessionKey(tx, serverID, hub, d, usedKeys)
		check(err)
		usedKeys[actualKey] = true
		seen[actualKey] = true
		var sampleID int64
		err = insertSample.QueryRow(
			serverID, hub, d.username, actualKey, d.clientIP, d.virtualIP, d.clientHost, d.started, stamp,
			d.packetSize, d.packetNum, d.down, d.up, d.startedTs,
			d.realName, d.note, d.ipLocation, d.ipISP, d.ipFull, d.protocol, d.serverPort, d.clientPort,
			d.sampled, d.sampled,
		).Scan(&sampleID)
		check(err)
		if d.sampled && sampleID > 0 {
			_, err = trafficStmt.Exec(sampleID, d.down, d.up, d.packetSize, stamp, stampTs)
			check(err)
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

	for i := 0; i < len(endedKeys); i += 400 {
		end := min(len(endedKeys), i+400)
		chunk := endedKeys[i:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, 0, 2+len(chunk))
		args = append(args, serverID, hub)
		for _, k := range chunk {
			args = append(args, k)
		}
		_, err = tx.Exec(`UPDATE VpnSessionSample SET EndedDate=LastSeenDate,EndedTs=COALESCE(NULLIF(EndedTs,0), unixepoch(LastSeenDate)) WHERE ServerID=? AND HubName=? AND EndedDate IS NULL AND SessionKey IN (`+placeholders+`)`, args...)
		check(err)
	}

	pendingObs := a.reconcileUserLogins(tx, serverID, hub, stamp, stampTime, sessionInterval, enumUsers, liveUserStarted)

	check(tx.Commit())

	if len(pendingObs) > 0 {
		a.loginObsMu.Lock()
		if a.loginObs == nil {
			a.loginObs = make(map[string]userLoginObservation)
		}
		for k, v := range pendingObs {
			a.loginObs[k] = v
		}
		a.loginObsMu.Unlock()
	}
}

func (a *App) reconcileUserLogins(tx *sql.Tx, serverID int64, hub, stamp string, stampTime time.Time, sessionInterval int64, users []M, liveUserStarted map[string][]time.Time) map[string]userLoginObservation {
	if len(users) == 0 {
		return nil
	}
	matchWindowSec := max(sessionInterval*2, int64(120))
	pending := make(map[string]userLoginObservation, len(users))

	a.loginObsMu.Lock()
	existingObs := make(map[string]userLoginObservation, len(users))
	for _, u := range users {
		name := strings.TrimSpace(str(u["Name_str"]))
		if name == "" {
			continue
		}
		obsKey := fmt.Sprintf("%d\x00%s\x00%s", serverID, hub, strings.ToLower(name))
		if prev, ok := a.loginObs[obsKey]; ok {
			existingObs[obsKey] = prev
		}
	}
	a.loginObsMu.Unlock()

	for _, u := range users {
		name := strings.TrimSpace(str(u["Name_str"]))
		if name == "" {
			continue
		}
		uFake := M{"Username_str": name}
		if !isClientSessionRPC(uFake) {
			continue
		}
		numLogin := integer(u["NumLogin_u32"])
		lastLogin := parseTime(str(u["LastLoginTime_dt"])).UTC()
		if lastLogin.IsZero() || lastLogin.Year() < 1990 {
			lastLogin = time.Time{}
		}
		sendBytes, recvBytes, validCounters := userTrafficCounters(u)

		obsKey := fmt.Sprintf("%d\x00%s\x00%s", serverID, hub, strings.ToLower(name))
		prev, seenBefore := existingObs[obsKey]
		if !seenBefore {
			var sBytes, rBytes, prevLogins int64
			var sDate string
			if err := tx.QueryRow(`SELECT SendBytes,RecvBytes,NumLogin,SampledDate FROM UserTrafficSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE ORDER BY UserTrafficSampleID DESC LIMIT 1`, serverID, hub, name).Scan(&sBytes, &rBytes, &prevLogins, &sDate); err == nil {
				if parsed := parseTime(sDate).UTC(); !parsed.IsZero() {
					prev = userLoginObservation{
						numLogin:      prevLogins,
						send:          sBytes,
						recv:          rBytes,
						validCounters: true,
						seenAt:        parsed,
					}
					seenBefore = true
				}
			}
		}

		pending[obsKey] = userLoginObservation{
			numLogin:      numLogin,
			lastLogin:     lastLogin,
			send:          sendBytes,
			recv:          recvBytes,
			validCounters: validCounters,
			seenAt:        stampTime,
		}

		if !seenBefore {
			continue
		}

		loginDetected := numLogin > prev.numLogin ||
			(!lastLogin.IsZero() && !prev.lastLogin.IsZero() && lastLogin.After(prev.lastLogin)) ||
			(!lastLogin.IsZero() && prev.lastLogin.IsZero() && !prev.seenAt.IsZero() && lastLogin.After(prev.seenAt))
		if !loginDetected {
			continue
		}

		loginAt := lastLogin
		if loginAt.IsZero() || loginAt.After(stampTime.Add(30*time.Second)) {
			loginAt = stampTime
		}

		// Skip if a live session in this tick already covers this login.
		uLower := strings.ToLower(name)
		coveredByLive := false
		for _, liveAt := range liveUserStarted[uLower] {
			diff := liveAt.Sub(loginAt)
			if diff < 0 {
				diff = -diff
			}
			if diff <= time.Duration(matchWindowSec)*time.Second {
				coveredByLive = true
				break
			}
		}
		if coveredByLive {
			continue
		}

		loginISO := loginAt.UTC().Format(time.RFC3339Nano)
		var existingID int64
		err := tx.QueryRow(
			`SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE AND (EndedDate IS NULL OR ABS(julianday(StartedDate)-julianday(?))*86400<=? OR (julianday(StartedDate)<=julianday(?) AND julianday(COALESCE(EndedDate,LastSeenDate))>=julianday(?))) LIMIT 1`,
			serverID, hub, name, loginISO, matchWindowSec, loginISO, loginISO,
		).Scan(&existingID)
		if err == nil {
			continue
		}

		var down, up, total int64
		if validCounters && prev.validCounters && recvBytes >= prev.recv && sendBytes >= prev.send {
			rawDown := recvBytes - prev.recv
			rawUp := sendBytes - prev.send
			prevTs := prev.seenAt.Unix()
			// Subtract traffic already accounted for by concurrent/overlapping sessions of this user since prev.seenAt.
			var coveredDown, coveredUp int64
			_ = tx.QueryRow(
				`SELECT COALESCE(SUM(CASE WHEN COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) >= ? THEN DownloadBytes ELSE 0 END), 0),
				        COALESCE(SUM(CASE WHEN COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) >= ? THEN UploadBytes ELSE 0 END), 0)
				 FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE AND COALESCE(EndedTs, unixepoch(COALESCE(EndedDate,LastSeenDate))) >= ?`,
				prevTs, prevTs, serverID, hub, name, prevTs,
			).Scan(&coveredDown, &coveredUp)
			var activeOlder int64
			_ = tx.QueryRow(
				`SELECT COUNT(*) FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND UserName=? COLLATE NOCASE AND COALESCE(NULLIF(StartedTs,0), unixepoch(StartedDate)) < ? AND COALESCE(EndedTs, unixepoch(COALESCE(EndedDate,LastSeenDate))) >= ?`,
				serverID, hub, name, prevTs, prevTs,
			).Scan(&activeOlder)
			if activeOlder == 0 {
				down = max(int64(0), rawDown-coveredDown)
				up = max(int64(0), rawUp-coveredUp)
				total = down + up
			}
		}

		shortKey := fmt.Sprintf("short:%s:%s:%d", uLower, loginAt.UTC().Format("20060102T150405.000Z"), numLogin)
		_, err = tx.Exec(
			`INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,VirtualIP,ClientHostname,StartedDate,EndedDate,LastSeenDate,BytesTotal,PacketsTotal,DownloadBytes,UploadBytes,StartedTs,EndedTs,RealName,Note) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,HubName,SessionKey) DO NOTHING`,
			serverID, hub, name, shortKey, "", "", "", loginISO, stamp, stamp, total, 0, down, up, loginAt.Unix(), stampTime.Unix(), str(u["Realname_utf"]), str(u["Note_utf"]),
		)
		check(err)
	}
	return pending
}
