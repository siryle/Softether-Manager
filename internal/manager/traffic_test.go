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
