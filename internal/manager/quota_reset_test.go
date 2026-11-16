package manager

import (
	"context"
	"net/url"
	"testing"
)

func TestQuotaResetKeepsActiveSessions(t *testing.T) {
	for _, subject := range []string{"user", "hub"} {
		for _, scenario := range []string{"no limit", "existing limit", "set limit with reset"} {
			t.Run(subject+"/"+scenario, func(t *testing.T) {
				a := testApp(t)
				token := admin(t, a)
				fake := attachSoftEther(t, a)
				hub, name := "Office / 上海", ""
				path := "/api/v1/quotas/" + subject + "/" + url.PathEscape(hub)
				if subject == "user" {
					name = "Alice"
					path += "/Alice"
				}
				if scenario == "existing limit" {
					a.saveQuota(1, subject, hub, name, 524, "total", true)
				}
				method, target, body := "POST", path+"/reset", M(nil)
				if scenario == "set limit with reset" {
					method, target = "PUT", path
					body = M{"limit": 0.0005, "unit": "MB", "reset": true}
				}
				status, out, _ := call(t, a, method, target, body, token)
				if status != 200 || integer(out["used_bytes"]) != 0 || boolean(out["blocked"]) {
					t.Fatal("reset did not start a fresh cycle", status, out)
				}
				fake.mu.Lock()
				for _, req := range fake.calls {
					switch req["method"] {
					case "SetUser", "DeleteSession", "SetHubOnline":
						t.Errorf("reset changed the VPN connection: %s", req["method"])
					}
				}
				connected := fake.hubOnline && fake.userAccess
				fake.send += 1000
				fake.mu.Unlock()
				if !connected {
					t.Fatal("reset disconnected the active VPN user")
				}
				// Limits still apply to traffic transmitted after the new baseline.
				a.quotaTick(context.Background())
				out = quotaPublic(a.quotaRow(1, subject, hub, name))
				if boolean(out["blocked"]) != (scenario != "no limit") {
					t.Fatal("reset disabled normal quota enforcement", out)
				}
			})
		}
	}
}

func TestQuotaResetFlagValidatedBeforeSaving(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	attachSoftEther(t, a)
	path := "/api/v1/quotas/user/" + url.PathEscape("Office / 上海") + "/Alice"
	status, out, _ := call(t, a, "PUT", path, M{"limit": 1, "unit": "GB", "reset": "true"}, token)
	if status != 422 {
		t.Fatal("a non-boolean reset flag was accepted", status, out)
	}
	if a.quotaRow(1, "user", "Office / 上海", "Alice") != nil {
		t.Fatal("an invalid reset request partially saved its quota")
	}
}
