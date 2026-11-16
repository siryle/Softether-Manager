package manager

import (
	"fmt"
	"strconv"
	"time"
)

type trafficCounter struct {
	at         time.Time
	send, recv int64
	reset      bool
}

// A counter reset or an empty baseline does not prove how much traffic moved.
func trafficDifference(current, previous trafficCounter, maxGap time.Duration) (int64, int64, bool) {
	elapsed := current.at.Sub(previous.at)
	if elapsed <= 0 || elapsed > maxGap || current.reset || previous.reset || (previous.send == 0 && previous.recv == 0 && (current.send > 0 || current.recv > 0)) {
		return 0, 0, false
	}
	return current.send - previous.send, current.recv - previous.recv, true
}

func (a *App) trafficUsage(serverID int64, kind, hub, name string, hours int64) M {
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	interval := max(int64(1), integer(a.setting("sample_interval_minutes"))) * 60
	maxGap := time.Duration(interval) * time.Second * 3 / 2
	table, id, sessions := "HubTrafficSample", "HubTrafficSampleID", "NumSessions"
	where := "ServerID=? AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<=julianday(?)"
	args := []any{serverID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if kind != "server" {
		where += " AND HubName=?"
		args = append(args, hub)
	}
	if kind == "user" {
		table, id, sessions = "UserTrafficSample", "UserTrafficSampleID", "0"
		where += " AND UserName=? COLLATE NOCASE"
		args = append(args, name)
	}
	rows, err := a.db.Query("SELECT HubName,SendBytes,RecvBytes,"+sessions+",SampledDate FROM "+table+" WHERE "+where+" ORDER BY julianday(SampledDate),"+id, args...)
	check(err)
	defer rows.Close()
	previous := map[string]trafficCounter{}
	points := []M{}
	var sent, received int64
	var latest time.Time
	for rows.Next() {
		var source, stamp string
		var send, recv, count int64
		check(rows.Scan(&source, &send, &recv, &count, &stamp))
		at := parseTime(stamp).UTC()
		if at.IsZero() || send < 0 || recv < 0 {
			continue
		}
		if !at.Equal(latest) {
			points = append(points, M{"t": at.Format(time.RFC3339Nano), "send": nil, "recv": nil})
			latest = at
		}
		point := points[len(points)-1]
		if kind != "user" {
			point["sessions"] = integer(point["sessions"]) + count
		}
		current := trafficCounter{at: at, send: send, recv: recv}
		if before, exists := previous[source]; exists {
			current.reset = send < before.send || recv < before.recv
			if up, down, valid := trafficDifference(current, before, maxGap); valid {
				point["send"] = integer(point["send"]) + up
				point["recv"] = integer(point["recv"]) + down
				sent += up
				received += down
			}
		}
		previous[source] = current
	}
	check(rows.Err())
	count := len(points)
	if count > 0 && points[0]["send"] == nil && points[0]["recv"] == nil {
		points = points[1:]
	}
	return M{"hours": hours, "start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "sample_interval_seconds": interval, "points": points, "total_send": sent, "total_recv": received, "samples": count}
}

// An incomplete RPC result is a missing sample, not a zero counter.
func sampledTraffic(m M, prefix string) (int64, int64, bool) {
	var totals [2]int64
	for direction, label := range []string{"Send.", "Recv."} {
		for _, kind := range []string{"UnicastBytes_u64", "BroadcastBytes_u64"} {
			value, exists := m[prefix+label+kind]
			if !exists && kind == "BroadcastBytes_u64" {
				continue
			}
			n, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
			if !exists || err != nil || n < 0 || totals[direction] > 1<<63-1-n {
				return 0, 0, false
			}
			totals[direction] += n
		}
	}
	return totals[0], totals[1], true
}
