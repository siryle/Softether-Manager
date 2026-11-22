package manager

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	source              string
	send, recv          int64
	deltaSend, deltaRecv int64
	hasDelta            bool
	count               int64
	at                  time.Time
	nextStep            time.Duration
}

func (a *App) trafficUsageFromRollup(serverID int64, kind, hub, name string, hours int64, start, end time.Time) (M, bool) {
	subject := "hub"
	where := "ServerID=? AND SubjectType=?"
	args := []any{serverID, subject}
	rawTable := "HubTrafficSample"
	rawWhere := "ServerID=?"
	rawArgs := []any{serverID}
	if kind != "server" {
		where += " AND HubName=?"
		args = append(args, hub)
		rawWhere += " AND HubName=?"
		rawArgs = append(rawArgs, hub)
	}
	if kind == "user" {
		subject = "user"
		args[1] = "user"
		where += " AND UserKey=?"
		args = append(args, fold(name))
		rawTable = "UserTrafficSample"
		rawWhere += " AND UserName=? COLLATE NOCASE"
		rawArgs = append(rawArgs, name)
	}

	rdb := a.readDB()
	var minBucket sql.NullInt64
	var cnt int64
	err := rdb.QueryRow("SELECT MIN(BucketTs), COUNT(*) FROM TrafficRollupHourly WHERE "+where+" AND BucketTs>=? AND BucketTs<=?", append(append([]any{}, args...), start.Unix()-start.Unix()%3600, end.Unix())...).Scan(&minBucket, &cnt)
	if err != nil || cnt == 0 || !minBucket.Valid {
		return nil, false
	}

	startTs := start.Unix()
	if minBucket.Int64 > startTs {
		// Check if un-rolled-up raw samples exist in [start, minBucket).
		var preCnt int64
		_ = rdb.QueryRow("SELECT COUNT(*) FROM "+rawTable+" WHERE "+rawWhere+" AND ((SampledTs>0 AND SampledTs>=? AND SampledTs<?) OR (SampledTs=0 AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<julianday(?)))",
			append(append([]any{}, rawArgs...), startTs, minBucket.Int64, start.Format(time.RFC3339Nano), time.Unix(minBucket.Int64, 0).UTC().Format(time.RFC3339Nano))...).Scan(&preCnt)
		if preCnt > 0 {
			return nil, false
		}
	}

	startBucket := startTs - (startTs % 3600)
	endBucket := end.Unix()
	qArgs := append(append([]any{}, args...), startBucket, endBucket)
	rows, err := rdb.Query("SELECT BucketTs, SUM(SendBytes), SUM(RecvBytes), MAX(MaxSessions), SUM(SampleCount) FROM TrafficRollupHourly WHERE "+where+" AND BucketTs>=? AND BucketTs<=? GROUP BY BucketTs ORDER BY BucketTs", qArgs...)
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	points := make([]M, 0, 256)
	var sent, received int64
	var prevBucket int64
	var sampleCount int64
	for rows.Next() {
		var bTs, sBytes, rBytes, sess, n int64
		if err := rows.Scan(&bTs, &sBytes, &rBytes, &sess, &n); err != nil {
			return nil, false
		}
		if n <= 0 {
			continue
		}
		if prevBucket > 0 && bTs-prevBucket > 3600 {
			gapT := time.Unix(prevBucket+3600, 0).UTC()
			if !gapT.After(end) {
				points = append(points, M{"t": gapT.Format(time.RFC3339Nano), "send": nil, "recv": nil})
			}
		}
		bt := time.Unix(bTs+1800, 0).UTC()
		if bt.Before(start) {
			bt = start
		}
		if bt.After(end) {
			bt = end
		}
		p := M{"t": bt.Format(time.RFC3339Nano), "send": sBytes, "recv": rBytes}
		if kind != "user" {
			p["sessions"] = sess
		}
		points = append(points, p)
		sent += sBytes
		received += rBytes
		sampleCount += n
		prevBucket = bTs
	}
	if rows.Err() != nil || len(points) == 0 {
		return nil, false
	}
	points, effectiveInterval := downsampleTrafficPoints(points, hours, 3600, start, end)
	return M{"hours": hours, "start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "sample_interval_seconds": effectiveInterval, "points": points, "total_send": sent, "total_recv": received, "samples": sampleCount}, true
}

func (a *App) trafficUsage(serverID int64, kind, hub, name string, hours int64) M {
	end := time.Now().UTC()
	start := end.Add(-time.Duration(hours) * time.Hour)
	if hours > 24 {
		if fast, ok := a.trafficUsageFromRollup(serverID, kind, hub, name, hours, start, end); ok {
			return fast
		}
	}
	interval := max(int64(1), integer(a.setting("sample_interval_minutes"))) * 60
	maxGap := time.Duration(interval)*time.Second*3/2 + 30*time.Second
	lookback := max(maxGap, 15*time.Minute)
	table, id, sessions := "HubTrafficSample", "HubTrafficSampleID", "NumSessions"
	lookbackStart := start.Add(-lookback)
	where := "ServerID=? AND ((SampledTs>0 AND SampledTs>=? AND SampledTs<=?) OR (SampledTs=0 AND julianday(SampledDate)>=julianday(?) AND julianday(SampledDate)<=julianday(?)))"
	args := []any{serverID, lookbackStart.Unix(), end.Unix(), lookbackStart.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if kind != "server" {
		where += " AND HubName=?"
		args = append(args, hub)
	}
	if kind == "user" {
		table, id, sessions = "UserTrafficSample", "UserTrafficSampleID", "0"
		where += " AND UserName=? COLLATE NOCASE"
		args = append(args, name)
	}
	rows, err := a.readDB().Query("SELECT HubName,SendBytes,RecvBytes,"+sessions+",SampledDate,SampledTs,DeltaSendBytes,DeltaRecvBytes,HasDelta FROM "+table+" WHERE "+where+" ORDER BY COALESCE(NULLIF(SampledTs,0), unixepoch(SampledDate)),"+id, args...)
	check(err)
	defer rows.Close()
	var scanned []trafficSampleRow
	for rows.Next() {
		var source, stamp string
		var send, recv, count, ts, dSend, dRecv, hasDelta int64
		check(rows.Scan(&source, &send, &recv, &count, &stamp, &ts, &dSend, &dRecv, &hasDelta))
		at := parseTime(stamp).UTC()
		if at.IsZero() && ts > 0 {
			at = time.Unix(ts, 0).UTC()
		}
		if at.IsZero() || send < 0 || recv < 0 {
			continue
		}
		scanned = append(scanned, trafficSampleRow{source: source, send: send, recv: recv, deltaSend: dSend, deltaRecv: dRecv, hasDelta: hasDelta != 0, count: count, at: at})
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
		if row.hasDelta {
			up, down := max(int64(0), row.deltaSend), max(int64(0), row.deltaRecv)
			point["send"] = integer(point["send"]) + up
			point["recv"] = integer(point["recv"]) + down
			sent += up
			received += down
			if hasBefore {
				if step := int64(current.rawStep.Seconds()); step > effectiveInterval && current.rawStep <= maxGap {
					effectiveInterval = step
				}
			}
		} else if hasBefore {
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
	points, effectiveInterval = downsampleTrafficPoints(points, hours, effectiveInterval, start, end)
	return M{"hours": hours, "start": start.Format(time.RFC3339Nano), "end": end.Format(time.RFC3339Nano), "sample_interval_seconds": effectiveInterval, "points": points, "total_send": sent, "total_recv": received, "samples": count}
}

const maxChartPoints = 360

// downsampleTrafficPoints aggregates dense series (e.g. 30d or 1m intervals) into
// evenly spaced buckets while preserving exact totals and nil gap boundaries.
func downsampleTrafficPoints(points []M, hours, effectiveInterval int64, start, end time.Time) ([]M, int64) {
	if len(points) <= maxChartPoints {
		return points, effectiveInterval
	}
	step := max(int64(1), effectiveInterval)
	target := max(step, (hours*3600+maxChartPoints-1)/maxChartPoints)
	bucketSec := ((target + step - 1) / step) * step

	out := make([]M, 0, maxChartPoints+8)
	var curIdx int64
	hasCur := false
	lastWasGap := false

	for _, p := range points {
		if p["send"] == nil || p["recv"] == nil {
			if hasCur || !lastWasGap {
				out = append(out, M{"t": p["t"], "send": nil, "recv": nil})
				lastWasGap = true
			}
			hasCur = false
			continue
		}
		at := parseTime(str(p["t"])).UTC()
		if at.IsZero() {
			continue
		}
		bIdx := at.Unix() / bucketSec
		if !hasCur || bIdx != curIdx {
			bt := time.Unix(bIdx*bucketSec+bucketSec/2, 0).UTC()
			if bt.Before(start) {
				bt = start
			}
			if bt.After(end) {
				bt = end
			}
			bucket := M{
				"t":    bt.Format(time.RFC3339Nano),
				"send": integer(p["send"]),
				"recv": integer(p["recv"]),
			}
			if sess, ok := p["sessions"]; ok {
				bucket["sessions"] = integer(sess)
			}
			out = append(out, bucket)
			curIdx = bIdx
			hasCur = true
			lastWasGap = false
		} else {
			cur := out[len(out)-1]
			cur["send"] = integer(cur["send"]) + integer(p["send"])
			cur["recv"] = integer(cur["recv"]) + integer(p["recv"])
			if sess, ok := p["sessions"]; ok {
				cur["sessions"] = max(integer(cur["sessions"]), integer(sess))
			}
		}
	}
	return out, max(effectiveInterval, bucketSec)
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

const (
	liveTrafficPreferredSpan = 10 * time.Second // matches SoftEther's INCREMENT_TRAFFIC_INTERVAL
	liveTrafficMinSpan       = 2 * time.Second
	liveTrafficMaxAge        = 10 * time.Minute
	liveTrafficMaxPoints     = 32
)

type liveTrafficPoint struct {
	at         time.Time
	send, recv int64
}

func sumUserListTraffic(users []M) (send, recv int64, ok bool) {
	validUsers := 0
	for _, u := range users {
		s, r, valid := userTrafficCounters(u)
		if !valid {
			continue
		}
		validUsers++
		send += s
		recv += r
	}
	if len(users) > 0 && validUsers == 0 {
		return 0, 0, false
	}
	return send, recv, true
}

func (a *App) observeHubLiveTraffic(serverID int64, hub string, send, recv int64, at time.Time) (sendRate, recvRate float64, hasRate bool) {
	if serverID <= 0 || strings.TrimSpace(hub) == "" {
		return 0, 0, false
	}
	key := userMetaCacheKey(serverID, hub)
	a.liveTrafficMu.Lock()
	defer a.liveTrafficMu.Unlock()
	if a.liveTraffic == nil {
		a.liveTraffic = make(map[string][]liveTrafficPoint)
	}
	pts := a.liveTraffic[key]
	cutoff := at.Add(-liveTrafficMaxAge)
	keep := 0
	for _, p := range pts {
		if !p.at.Before(cutoff) && !p.at.After(at) {
			pts[keep] = p
			keep++
		}
	}
	pts = pts[:keep]

	// Pick the newest reference >= 10s old so SoftEther's 10s flush interval does not jitter or spike on rapid refreshes.
	refIdx := -1
	for i := len(pts) - 1; i >= 0; i-- {
		if at.Sub(pts[i].at) >= liveTrafficPreferredSpan {
			refIdx = i
			break
		}
	}
	if refIdx < 0 {
		for i := 0; i < len(pts); i++ {
			if at.Sub(pts[i].at) >= liveTrafficMinSpan {
				refIdx = i
				break
			}
		}
	}
	if refIdx >= 0 {
		ref := pts[refIdx]
		if elapsed := at.Sub(ref.at).Seconds(); elapsed > 0 {
			if send >= ref.send {
				sendRate = float64(send-ref.send) / elapsed
			}
			if recv >= ref.recv {
				recvRate = float64(recv-ref.recv) / elapsed
			}
			hasRate = true
		}
	}

	if len(pts) == 0 || at.Sub(pts[len(pts)-1].at) >= liveTrafficMinSpan {
		pts = append(pts, liveTrafficPoint{at: at, send: send, recv: recv})
		if len(pts) > liveTrafficMaxPoints {
			pts = append([]liveTrafficPoint(nil), pts[len(pts)-liveTrafficMaxPoints:]...)
		}
	}
	a.liveTraffic[key] = pts
	return sendRate, recvRate, hasRate
}

func (a *App) invalidateLiveTraffic(serverID int64) {
	a.liveTrafficMu.Lock()
	defer a.liveTrafficMu.Unlock()
	if a.liveTraffic == nil {
		return
	}
	prefix := fmt.Sprintf("%d\x00", serverID)
	for k := range a.liveTraffic {
		if strings.HasPrefix(k, prefix) {
			delete(a.liveTraffic, k)
		}
	}
}
