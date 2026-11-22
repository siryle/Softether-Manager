package manager

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func insertHistoryCounter(a *App, stamp string, sent, received int64) {
	execute(a.db, `INSERT INTO HubTrafficSample(ServerID,HubName,SendBytes,RecvBytes,SampledDate) VALUES(1,'DEFAULT',?,?,?)`, sent, received, stamp)
	execute(a.db, `INSERT INTO UserTrafficSample(ServerID,HubName,UserName,SendBytes,RecvBytes,SampledDate) VALUES(1,'DEFAULT','admin',?,?,?)`, sent, received, stamp)
}

func TestTrafficHistoryDoesNotInventUnrecordedTraffic(t *testing.T) {
	const lifetime = int64(1540) << 30
	for _, scenario := range []string{"empty", "one baseline", "long gap", "zero sample", "old zero sample", "initial zero", "mixed timestamps"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			now := time.Now().UTC().Truncate(time.Second)
			put := func(minutes int, value int64) {
				insertHistoryCounter(a, now.Add(time.Duration(minutes)*time.Minute).Format(time.RFC3339), value, value)
			}
			var expected int64
			switch scenario {
			case "one baseline":
				put(-5, lifetime)
			case "long gap":
				put(-12*60, lifetime)
				put(-15, lifetime+100)
				put(-10, lifetime+110)
				put(-5, lifetime+120)
				expected = 20
			case "zero sample":
				for i, value := range []int64{lifetime, lifetime + 10, 0, lifetime + 20, lifetime + 30, lifetime + 40} {
					put(-30+i*5, value)
				}
				expected = 30
			case "old zero sample":
				for i, value := range []int64{lifetime, lifetime + 10, 0, lifetime + 20, lifetime + 30, lifetime + 40} {
					put(-12*60+i*5, value)
				}
				put(-15, lifetime+1000)
				put(-10, lifetime+1010)
				put(-5, lifetime+1020)
				expected = 20
			case "initial zero":
				for i, value := range []int64{0, lifetime, lifetime + 10, lifetime + 20} {
					put(-20+i*5, value)
				}
				expected = 20
			case "mixed timestamps":
				for i, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05", time.RFC3339Nano, "2006-01-02 15:04:05.999999Z07:00"} {
					at := now.Add(time.Duration(-25+i*5) * time.Minute)
					if i >= 3 {
						at = at.In(time.FixedZone("UTC+8", 8*3600))
					}
					insertHistoryCounter(a, at.Format(layout), int64(100*(i+1)), int64(100*(i+1)))
				}
				expected = 400
			}
			// Future samples cannot extend or inflate any selected window.
			insertHistoryCounter(a, now.Add(time.Hour).Format(time.RFC3339), lifetime*10, lifetime*10)
			for _, path := range []string{"/api/v1/server/traffic", "/api/v1/hubs/DEFAULT/traffic", "/api/v1/hubs/DEFAULT/users/admin/usage"} {
				for _, hours := range []int{6, 24, 168, 720} {
					status, out, _ := call(t, a, "GET", fmt.Sprintf("%s?hours=%d", path, hours), nil, token)
					want := expected
					if scenario == "old zero sample" && hours > 6 {
						want += 30
					}
					if status != 200 || integer(out["total_send"]) != want || integer(out["total_recv"]) != want {
						t.Fatalf("%s (%dh) invented traffic: status=%d %v", path, hours, status, out)
					}
					span := parseTime(str(out["end"])).Sub(parseTime(str(out["start"])))
					if span != time.Duration(hours)*time.Hour {
						t.Fatalf("wrong time window for %s: %v", path, span)
					}
					if expected == 0 && len(list(out["points"])) != 0 {
						t.Fatalf("%s drew a series without two observations", path)
					}
					if scenario == "zero sample" {
						gaps := 0
						for _, point := range list(out["points"]) {
							if point["send"] == nil && point["recv"] == nil {
								gaps++
							}
						}
						if gaps != 2 {
							t.Fatalf("%s did not preserve the reset gap: %v", path, out)
						}
					}
				}
			}
		})
	}
}

func TestIncompleteTrafficResponseDoesNotCreateZeroHistory(t *testing.T) {
	a := testApp(t)
	fake := attachSoftEther(t, a)
	fake.mu.Lock()
	fake.missingTraffic = true
	fake.mu.Unlock()
	a.sampleTraffic(context.Background())
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample"} {
		if integer(one(a.db, `SELECT COUNT(*) n FROM `+table)["n"]) != 0 {
			t.Fatal("an incomplete response created a false zero sample", table)
		}
	}
	fake.mu.Lock()
	fake.missingTraffic = false
	fake.mu.Unlock()
	a.sampleTraffic(context.Background())
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample"} {
		if integer(one(a.db, `SELECT COUNT(*) n FROM `+table)["n"]) != 1 {
			t.Fatal("valid traffic sampling did not resume", table)
		}
	}
}

func TestTrafficHistoryPreservesZeroStartPostRestartAndPreWindowBaseline(t *testing.T) {
	t.Run("zero baseline to real traffic", func(t *testing.T) {
		a := testApp(t)
		token := admin(t, a)
		base := time.Now().UTC().Truncate(time.Second)
		insertHistoryCounter(a, base.Add(-15*time.Minute).Format(time.RFC3339), 0, 0)
		insertHistoryCounter(a, base.Add(-10*time.Minute).Format(time.RFC3339), 7_140_000, 51_300_000)
		insertHistoryCounter(a, base.Add(-5*time.Minute).Format(time.RFC3339), 8_000_000, 55_000_000)
		for _, path := range []string{"/api/v1/server/traffic", "/api/v1/hubs/DEFAULT/traffic", "/api/v1/hubs/DEFAULT/users/admin/usage"} {
			status, out, _ := call(t, a, "GET", path+"?hours=6", nil, token)
			if status != 200 || integer(out["total_send"]) != 8_000_000 || integer(out["total_recv"]) != 55_000_000 {
				t.Fatalf("%s dropped traffic starting from zero: status=%d out=%v", path, status, out)
			}
		}
	})

	t.Run("post restart increments are retained", func(t *testing.T) {
		a := testApp(t)
		token := admin(t, a)
		base := time.Now().UTC().Truncate(time.Second)
		vals := [][2]int64{{100_000, 200_000}, {150_000, 300_000}, {5_000, 10_000}, {25_000, 50_000}}
		for i, v := range vals {
			insertHistoryCounter(a, base.Add(time.Duration(-20+i*5)*time.Minute).Format(time.RFC3339), v[0], v[1])
		}
		status, out, _ := call(t, a, "GET", "/api/v1/hubs/DEFAULT/users/admin/usage?hours=6", nil, token)
		// (150k-100k) + (25k-5k) = 70k send; (300k-200k) + (50k-10k) = 140k recv
		if status != 200 || integer(out["total_send"]) != 70_000 || integer(out["total_recv"]) != 140_000 {
			t.Fatalf("post-restart delta dropped: status=%d out=%v", status, out)
		}
	})

	t.Run("pre-window baseline covers first interval and interval shrink preserves history", func(t *testing.T) {
		a := testApp(t)
		token := admin(t, a)
		base := time.Now().UTC().Truncate(time.Second)
		// Baseline is 2 minutes before the 6h window start; first in-window sample is 3 minutes after start.
		insertHistoryCounter(a, base.Add(-6*time.Hour-2*time.Minute).Format(time.RFC3339), 1_000, 2_000)
		insertHistoryCounter(a, base.Add(-6*time.Hour+3*time.Minute).Format(time.RFC3339), 1_500, 3_000)
		insertHistoryCounter(a, base.Add(-6*time.Hour+8*time.Minute).Format(time.RFC3339), 2_000, 4_500)
		// Lower sample_interval_minutes from 5 to 1 after historical 5-minute samples were recorded.
		a.set("sample_interval_minutes", 1)
		status, out, _ := call(t, a, "GET", "/api/v1/hubs/DEFAULT/traffic?hours=6", nil, token)
		if status != 200 || integer(out["total_send"]) != 1_000 || integer(out["total_recv"]) != 2_500 {
			t.Fatalf("lost pre-window or historical cadence traffic: status=%d out=%v", status, out)
		}
		if integer(out["sample_interval_seconds"]) < 300 {
			t.Fatalf("expected effective sample_interval_seconds >= 300 for 5m history, got %v", out["sample_interval_seconds"])
		}
	})
}

func TestTrafficUsageDownsamplesLongWindowsAndUsesReadOnlyPool(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	if a.rdb == nil || a.rdb == a.db {
		t.Fatal("expected separate read-only SQLite pool for file-backed DB")
	}
	if _, err := a.rdb.Exec(`INSERT INTO Setting(SettingKey,SettingValue,UpdatedDate) VALUES('test','1','2026-01-01')`); err == nil {
		t.Fatal("expected read-only DB pool to reject writes")
	}

	base := time.Now().UTC().Truncate(time.Minute)
	var sendTotal, recvTotal int64
	var expectedSend, expectedRecv int64
	// Insert 500 5-minute samples (~41.6 hours) with a 1-hour gap in the middle.
	for i := 0; i < 500; i++ {
		offsetMin := -500*5 + i*5
		if i >= 250 && i < 262 {
			continue // 1-hour gap
		}
		if i > 0 && i != 262 {
			sendTotal += 100
			recvTotal += 200
			expectedSend += 100
			expectedRecv += 200
		} else if i == 262 {
			// Counter continues across gap; gap itself must not be counted.
			sendTotal += 99999
			recvTotal += 99999
		}
		insertHistoryCounter(a, base.Add(time.Duration(offsetMin)*time.Minute).Format(time.RFC3339), sendTotal, recvTotal)
	}

	status, out, _ := call(t, a, "GET", "/api/v1/server/traffic?hours=720", nil, token)
	if status != 200 {
		t.Fatalf("status=%d out=%v", status, out)
	}
	if integer(out["total_send"]) != expectedSend || integer(out["total_recv"]) != expectedRecv {
		t.Fatalf("downsampling altered totals: got send=%d recv=%d, want send=%d recv=%d", integer(out["total_send"]), integer(out["total_recv"]), expectedSend, expectedRecv)
	}
	pts := list(out["points"])
	if len(pts) > 370 || len(pts) < 10 {
		t.Fatalf("expected downsampled points <= 370, got %d", len(pts))
	}
	hasGap := false
	for _, p := range pts {
		if p["send"] == nil && p["recv"] == nil {
			hasGap = true
			break
		}
	}
	if !hasGap {
		t.Fatal("downsampling lost the gap boundary")
	}
}

func TestObserveHubLiveTrafficComputesStableRatesAndHandlesDrops(t *testing.T) {
	a := testApp(t)
	base := time.Now()

	// First observation only establishes a baseline.
	if s, r, ok := a.observeHubLiveTraffic(1, "DEFAULT", 1000, 2000, base); ok || s != 0 || r != 0 {
		t.Fatalf("expected no rate on baseline, got send=%v recv=%v ok=%v", s, r, ok)
	}

	// After 10s (matching SoftEther's flush interval), exact B/s is computed.
	s, r, ok := a.observeHubLiveTraffic(1, "DEFAULT", 3000, 12000, base.Add(10*time.Second))
	if !ok || s != 200 || r != 1000 {
		t.Fatalf("expected 200/1000 B/s over 10s, got send=%v recv=%v ok=%v", s, r, ok)
	}

	// Rapid 1s refresh with unchanged counters must skip the 1s point and keep referencing the >=10s baseline (no fake 0 drop).
	s, r, ok = a.observeHubLiveTraffic(1, "DEFAULT", 3000, 12000, base.Add(11*time.Second))
	if !ok || s < 180 || s > 183 || r < 908 || r > 910 {
		t.Fatalf("rapid refresh jittered rate: send=%v recv=%v ok=%v", s, r, ok)
	}

	// Counter drop (e.g. user deleted or server restarted) clamps cleanly to 0 without negative values.
	s, r, ok = a.observeHubLiveTraffic(1, "DEFAULT", 100, 200, base.Add(25*time.Second))
	if !ok || s != 0 || r != 0 {
		t.Fatalf("expected drop to clamp to 0 B/s, got send=%v recv=%v ok=%v", s, r, ok)
	}

	// Invalidating the server clears its live rate baseline.
	a.invalidateLiveTraffic(1)
	if _, _, ok := a.observeHubLiveTraffic(1, "DEFAULT", 500, 1000, base.Add(40*time.Second)); ok {
		t.Fatal("expected invalidated server to require a fresh baseline")
	}
}
