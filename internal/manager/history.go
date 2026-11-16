package manager

import (
	"context"
	"fmt"
	"golang.org/x/text/cases"
	"log"
	"net/http"
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
	for _, userOnly := range []bool{false, true} {
		path := "/hubs/{hub}/session-history"
		if userOnly {
			path = "/hubs/{hub}/users/{name}/session-history"
		}
		a.endpoint("GET "+path, func(_ http.ResponseWriter, r *http.Request, _ M) any {
			serverID := a.serverIDFromContext(r.Context())
			statement := `SELECT VpnSessionSampleID id,HubName hub,UserName username,ClientIP client_ip,ClientHostname client_hostname,StartedDate started_date,EndedDate ended_date,LastSeenDate last_seen_date,BytesTotal bytes_total,PacketsTotal packets_total,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE ServerID=? AND HubName=?`
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
			return query(a.db, statement, args...)
		})
	}
	a.endpoint("GET /hubs/{hub}/session-history/{session_id}/usage", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		session := one(a.db, `SELECT VpnSessionSampleID id,UserName username,ClientIP client_ip,StartedDate started_date,EndedDate ended_date,DownloadBytes download_bytes,UploadBytes upload_bytes FROM VpnSessionSample WHERE ServerID=? AND VpnSessionSampleID=? AND HubName=?`, serverID, r.PathValue("session_id"), r.PathValue("hub"))
		if session == nil {
			fail(404, "No such session in this hub's history.")
		}
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
}
func bytesFor(m M, prefix string) (int64, int64) {
	return integer(m[prefix+"Send.UnicastBytes_u64"]) + integer(m[prefix+"Send.BroadcastBytes_u64"]), integer(m[prefix+"Recv.UnicastBytes_u64"]) + integer(m[prefix+"Recv.BroadcastBytes_u64"])
}
func (a *App) prune() {
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
						execute(a.db, `INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,NumSessions,NumUsers,SampledDate) VALUES(?,?,?,?,?,?,?)`, serverID, hub, send, recv, integer(status["NumSessions_u32"]), integer(status["NumUsers_u32"]), stamp)
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
						execute(a.db, `INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,NumLogin,SampledDate) VALUES(?,?,?,?,?,?,?)`, serverID, hub, name, sBytes, rBytes, integer(u["NumLogin_u32"]), stamp)
					}
				}()
			}
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
func (a *App) sampleHubSessions(ctx context.Context, serverID int64, hub string) {
	live := list(a.rpc(ctx, "EnumSession", M{"HubName_str": hub})["SessionList"])
	seen := map[string]bool{}
	stamp := now()
	series := boolean(a.setting("session_traffic_enabled"))
	for _, s := range live {
		key := str(s["UniqueId_bin"])
		if key == "" {
			continue
		}
		seen[key] = true
		var down, up int64
		sampled := false
		if series {
			func() {
				defer func() {
					if p := recover(); p != nil {
						log.Printf("session traffic %d/%s/%s: %v", serverID, hub, key, p)
					}
				}()
				status := a.rpc(ctx, "GetSessionStatus", M{"HubName_str": hub, "Name_str": s["Name_str"]})
				down, up = bytesFor(status, "")
				sampled = true
			}()
		}
		started := str(s["CreatedTime_dt"])
		if started == "" {
			started = stamp
		}
		execute(a.db, `INSERT INTO VpnSessionSample(ServerID,HubName,UserName,SessionKey,ClientIP,ClientHostname,StartedDate,LastSeenDate,BytesTotal,PacketsTotal,DownloadBytes,UploadBytes) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,HubName,SessionKey) DO UPDATE SET LastSeenDate=excluded.LastSeenDate,BytesTotal=excluded.BytesTotal,PacketsTotal=excluded.PacketsTotal,DownloadBytes=CASE WHEN ? THEN excluded.DownloadBytes ELSE VpnSessionSample.DownloadBytes END,UploadBytes=CASE WHEN ? THEN excluded.UploadBytes ELSE VpnSessionSample.UploadBytes END`, serverID, hub, str(s["Username_str"]), key, str(s["ClientIP_ip"]), str(s["Hostname_str"]), started, stamp, integer(s["PacketSize_u64"]), integer(s["PacketNum_u64"]), down, up, sampled, sampled)
		if sampled {
			row := one(a.db, `SELECT VpnSessionSampleID FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND SessionKey=?`, serverID, hub, key)
			if row != nil {
				execute(a.db, `INSERT INTO VpnSessionTrafficSample(VpnSessionSampleID,DownloadBytes,UploadBytes,TotalBytes,SampledDate) VALUES(?,?,?,?,?)`, row["VpnSessionSampleID"], down, up, integer(s["PacketSize_u64"]), stamp)
			}
		}
	}
	for _, row := range query(a.db, `SELECT SessionKey FROM VpnSessionSample WHERE ServerID=? AND HubName=? AND EndedDate IS NULL`, serverID, hub) {
		if !seen[str(row["SessionKey"])] {
			execute(a.db, `UPDATE VpnSessionSample SET EndedDate=LastSeenDate WHERE ServerID=? AND HubName=? AND SessionKey=? AND EndedDate IS NULL`, serverID, hub, row["SessionKey"])
		}
	}
}
