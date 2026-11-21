package manager

import (
	"fmt"
	"strconv"
	"time"
)

const maxSingleIntervalBytesPerSec int64 = 1_250_000_000 // 10 Gbps plausibility cap for unconfirmed 0 -> N baseline jumps

type trafficCounter struct {
	at               time.Time
	send, recv       int64
	reset            bool
	consecutiveZeros int
	preResetSend     int64
	preResetRecv     int64
	rawStep          time.Duration
	nextStep         time.Duration
}

// A counter reset or an empty baseline does not prove how much traffic moved.
func trafficDifference(current, previous trafficCounter, maxGap time.Duration) (int64, int64, bool) {
	elapsed := current.at.Sub(previous.at)
	if elapsed <= 0 || current.reset {
		return 0, 0, false
	}
	allowedGap := maxGap
	if elapsed > maxGap && elapsed <= 15*time.Minute {
		matchesPrev := previous.rawStep > maxGap && elapsed >= previous.rawStep/2 && elapsed <= previous.rawStep*3/2
		matchesNext := previous.rawStep == 0 && current.nextStep > maxGap && elapsed >= current.nextStep/2 && elapsed <= current.nextStep*3/2
		if matchesPrev || matchesNext {
			allowedGap = elapsed
		}
	}
	if elapsed > allowedGap {
		return 0, 0, false
	}
	// Legacy bogus 0 sample sandwiched inside a monotonic counter (e.g. lifetime -> 0 -> lifetime+delta).
	if previous.reset && previous.send == 0 && previous.recv == 0 && (current.send >= previous.preResetSend && current.recv >= previous.preResetRecv) {
		return 0, 0, false
	}
	if previous.send == 0 && previous.recv == 0 && (current.send > 0 || current.recv > 0) && previous.consecutiveZeros < 2 {
		secs := max(int64(1), int64(elapsed.Seconds()))
		if current.send+current.recv > maxSingleIntervalBytesPerSec*secs {
			return 0, 0, false
		}
	}
	return current.send - previous.send, current.recv - previous.recv, true
}

type trafficSampleRow struct {
	source     string
	send, recv int64
	count      int64
	at         time.Time
	nextStep   time.Duration
}

func (a *App) trafficUsage(serverID int64, kind, hub, name string, hours int64) M {
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	interval := max(int64(1), integer(a.setting("sample_interval_minutes"))) * 60
	maxGap := time.Duration(interval)*time.Second*3/2 + 30*time.Second
	lookback := max(maxGap, 15*time.Minute)
	table, id, sessions := "HubTrafficSample", "HubTrafficSampleID", "NumSessions"
	where := "ServerID=? AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<=julianday(?)"
	args := []any{serverID, start.Add(-lookback).Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
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
	var scanned []trafficSampleRow
	for rows.Next() {
		var source, stamp string
		var send, recv, count int64
		check(rows.Scan(&source, &send, &recv, &count, &stamp))
		at := parseTime(stamp).UTC()
		if at.IsZero() || send < 0 || recv < 0 {
			continue
		}
		scanned = append(scanned, trafficSampleRow{source: source, send: send, recv: recv, count: count, at: at})
	}
	check(rows.Err())
	nextAt := map[string]time.Time{}
	for i := len(scanned) - 1; i >= 0; i-- {
		s := scanned[i].source
		if prevNext, ok := nextAt[s]; ok {
			scanned[i].nextStep = prevNext.Sub(scanned[i].at)
		}
		nextAt[s] = scanned[i].at
	}
	previous := map[string]trafficCounter{}
	points := []M{}
	var sent, received int64
	var latest time.Time
	effectiveInterval := interval
	for _, row := range scanned {
		current := trafficCounter{at: row.at, send: row.send, recv: row.recv, nextStep: row.nextStep}
		if row.send == 0 && row.recv == 0 {
			current.consecutiveZeros = 1
		}
		before, hasBefore := previous[row.source]
		if hasBefore {
			current.rawStep = row.at.Sub(before.at)
			if row.send == 0 && row.recv == 0 && before.send == 0 && before.recv == 0 && !before.reset {
				current.consecutiveZeros = before.consecutiveZeros + 1
			}
			if row.send < before.send || row.recv < before.recv {
				current.reset = true
				current.preResetSend = before.send
				current.preResetRecv = before.recv
			}
		}
		if row.at.Before(start) {
			previous[row.source] = current
			continue
		}
		if !row.at.Equal(latest) {
			points = append(points, M{"t": row.at.Format(time.RFC3339Nano), "send": nil, "recv": nil})
			latest = row.at
		}
		point := points[len(points)-1]
		if kind != "user" {
			point["sessions"] = integer(point["sessions"]) + row.count
		}
		if hasBefore {
			if up, down, valid := trafficDifference(current, before, maxGap); valid {
				point["send"] = integer(point["send"]) + up
				point["recv"] = integer(point["recv"]) + down
				sent += up
				received += down
				if step := int64(current.rawStep.Seconds()); step > effectiveInterval {
					effectiveInterval = step
				}
			}
		}
		previous[row.source] = current
	}
	count := len(points)
	if count > 0 && points[0]["send"] == nil && points[0]["recv"] == nil {
		points = points[1:]
	}
	return M{"hours": hours, "start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "sample_interval_seconds": effectiveInterval, "points": points, "total_send": sent, "total_recv": received, "samples": count}
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
