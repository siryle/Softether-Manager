package manager

import (
	"context"
	"net/url"
	"testing"
)

func TestCancelQuotaRetainsFailedRestorationForRetry(t *testing.T) {
	for _, subject := range []string{"user", "hub"} {
		for _, action := range []string{"disable", "delete"} {
			for _, failure := range []string{"rejected", "acknowledged but unchanged"} {
				t.Run(subject+"/"+action+"/"+failure, func(t *testing.T) {
					a := testApp(t)
					token := admin(t, a)
					fake := attachSoftEther(t, a)
					hub, name := "Office / 上海", ""
					path := "/api/v1/quotas/" + subject + "/" + url.PathEscape(hub)
					if subject == "user" {
						name = "Alice"
						path += "/Alice"
					}
					status, out, _ := call(t, a, "PUT", path, M{"limit": 0.0005, "unit": "MB"}, token)
					if status != 200 || !boolean(out["blocked"]) {
						t.Fatal("initial quota did not block", status, out)
					}
					fake.mu.Lock()
					fake.failRestore = failure == "rejected"
					fake.ignoreRestore = failure == "acknowledged but unchanged"
					fake.mu.Unlock()
					if action == "delete" {
						status, out, _ = call(t, a, "DELETE", path, nil, token)
					} else {
						status, out, _ = call(t, a, "PUT", path+"/enabled", M{"enabled": false}, token)
					}
					if status < 400 {
						t.Fatal("cancellation reported success while VPN access was still blocked", status, out)
					}
					row := a.quotaRow(1, subject, hub, name)
					if row == nil || boolean(row["IsEnabled"]) || str(row["RestoreState"]) == "" {
						t.Fatal("failed restoration lost the cancellation or its retry state", row)
					}
					fake.mu.Lock()
					fake.failRestore, fake.ignoreRestore = false, false
					fake.mu.Unlock()
					a.quotaTick(context.Background())
					row = a.quotaRow(1, subject, hub, name)
					if str(row["RestoreState"]) != "" || str(row["EnforcedDate"]) != "" {
						t.Fatal("the background retry did not finish restoration", row)
					}
					fake.mu.Lock()
					allowed := fake.hubOnline && (!fake.userUsePolicy || fake.userAccess)
					fake.mu.Unlock()
					if !allowed {
						t.Fatal("the canceled quota left VPN access blocked")
					}
				})
			}
		}
	}
}

func TestCancelUserQuotaRestoresOriginalPolicy(t *testing.T) {
	for _, original := range []struct {
		name              string
		usePolicy, access bool
	}{{"inherited policy", false, false}, {"explicitly allowed", true, true}, {"already denied by administrator", true, false}} {
		t.Run(original.name, func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			fake := attachSoftEther(t, a)
			fake.mu.Lock()
			fake.userUsePolicy, fake.userAccess = original.usePolicy, original.access
			originalHash := fake.userHash
			fake.mu.Unlock()
			path := "/api/v1/quotas/user/" + url.PathEscape("Office / 上海") + "/Alice"
			status, out, _ := call(t, a, "PUT", path, M{"limit": 0.0005, "unit": "MB"}, token)
			if status != 200 || !boolean(out["blocked"]) {
				t.Fatal(status, out)
			}
			status, out, _ = call(t, a, "DELETE", path, nil, token)
			if status != 200 {
				t.Fatal(status, out)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if fake.userUsePolicy != original.usePolicy || (original.usePolicy && fake.userAccess != original.access) || fake.userHash != originalHash {
				t.Fatal("removing the quota changed the original policy or password")
			}
		})
	}
}

func TestResetDoesNotRewriteAlreadyRestoredVPNState(t *testing.T) {
	for _, subject := range []string{"user", "hub"} {
		t.Run(subject, func(t *testing.T) {
			a := testApp(t)
			token := admin(t, a)
			fake := attachSoftEther(t, a)
			hub, name, restore := "Office / 上海", "", `{"Online_bool":true}`
			path := "/api/v1/quotas/" + subject + "/" + url.PathEscape(hub)
			if subject == "user" {
				name, restore = "Alice", `{"UsePolicy_bool":false}`
				path += "/Alice"
				fake.mu.Lock()
				fake.userUsePolicy = false
				fake.mu.Unlock()
			}
			a.saveQuota(1, subject, hub, name, 0, "total", false)
			row := a.quotaRow(1, subject, hub, name)
			execute(a.db, `UPDATE TrafficQuota SET RestoreState=?,EnforcedDate=? WHERE TrafficQuotaID=?`, restore, now(), row["TrafficQuotaID"])
			status, out, _ := call(t, a, "POST", path+"/reset", nil, token)
			if status != 200 || integer(out["used_bytes"]) != 0 || boolean(out["blocked"]) {
				t.Fatal("reset failed on an already restored VPN state", status, out)
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			for _, req := range fake.calls {
				switch req["method"] {
				case "SetUser", "SetHubOnline", "DeleteSession":
					t.Fatalf("clearing a stale recovery record mutated VPN state: %s", req["method"])
				}
			}
		})
	}
}
