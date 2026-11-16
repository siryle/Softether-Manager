package manager

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sync"
)

func (a *App) quotaRow(serverID int64, subject, hub, name string) M {
	if serverID == 0 {
		serverID = 1
	}
	return one(a.db, `SELECT * FROM TrafficQuota WHERE ServerID=? AND SubjectType=? AND HubName=? AND UserKey=?`, serverID, subject, hub, fold(name))
}
func quotaPublic(row M) M {
	limit := integer(row["LimitBytes"])
	up, down := max(int64(0), integer(row["LastSendBytes"])-integer(row["BaseSendBytes"])), max(int64(0), integer(row["LastRecvBytes"])-integer(row["BaseRecvBytes"]))
	used := up + down
	if str(row["Metric"]) == "upload" {
		used = up
	}
	if str(row["Metric"]) == "download" {
		used = down
	}
	amount, unit := float64(limit)/(1<<20), "MB"
	for _, u := range []struct {
		name string
		size int64
	}{{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}} {
		v := float64(limit) / float64(u.size)
		if limit >= u.size && math.Abs(v-math.Round(v*100)/100) < 1e-9 {
			amount, unit = v, u.name
			break
		}
	}
	var remaining any
	percent := 0.
	if limit > 0 {
		remaining = max(int64(0), limit-used)
		percent = math.Round(math.Min(100, float64(used)/float64(limit)*100)*100) / 100
	}
	return M{"subject": row["SubjectType"], "hub": row["HubName"], "username": row["UserName"], "has_limit": limit > 0, "limit_bytes": limit, "limit": math.Round(amount*100) / 100, "unit": unit, "metric": row["Metric"], "enabled": boolean(row["IsEnabled"]), "upload_bytes": up, "download_bytes": down, "base_send_bytes": row["BaseSendBytes"], "base_recv_bytes": row["BaseRecvBytes"], "used_bytes": used, "remaining_bytes": remaining, "percent": percent, "exceeded": str(row["ExceededDate"]) != "", "exceeded_date": row["ExceededDate"], "blocked": str(row["EnforcedDate"]) != "", "blocked_date": row["EnforcedDate"], "cycle_start": row["CycleStartDate"], "updated_date": row["UpdatedDate"]}
}
func (a *App) saveQuota(serverID int64, subject, hub, name string, limit int64, metric string, enabled bool) {
	if serverID == 0 {
		serverID = 1
	}
	if metric != "total" && metric != "upload" && metric != "download" {
		fail(422, "Metric must be total, upload or download.")
	}
	execute(a.db, `INSERT INTO TrafficQuota(ServerID,SubjectType,HubName,UserName,UserKey,LimitBytes,Metric,IsEnabled,CycleStartDate,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(ServerID,SubjectType,HubName,UserKey) DO UPDATE SET UserName=excluded.UserName,LimitBytes=excluded.LimitBytes,Metric=excluded.Metric,IsEnabled=excluded.IsEnabled,UpdatedDate=excluded.UpdatedDate`, serverID, subject, hub, name, fold(name), limit, metric, enabled, now(), now(), now())
}
func (a *App) requireReleasedQuotas(serverID int64) {
	if one(a.db, `SELECT 1 FROM TrafficQuota WHERE ServerID=? AND RestoreState!='' LIMIT 1`, serverID) != nil {
		fail(409, "Release this server's enforced quotas before changing its address, disabling it, or removing the connection.")
	}
}
func (a *App) quotaLock(serverID int64) *sync.Mutex {
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	if a.quotaLocks == nil {
		a.quotaLocks = make(map[int64]*sync.Mutex)
	}
	if a.quotaLocks[serverID] == nil {
		a.quotaLocks[serverID] = &sync.Mutex{}
	}
	return a.quotaLocks[serverID]
}
func (a *App) observeQuotaLocked(serverID int64, subject, hub, name string, send, recv int64) {
	if serverID == 0 {
		serverID = 1
	}
	execute(a.db, `UPDATE TrafficQuota SET BaseSendBytes=CASE WHEN ?<LastSendBytes THEN BaseSendBytes-LastSendBytes ELSE BaseSendBytes END,BaseRecvBytes=CASE WHEN ?<LastRecvBytes THEN BaseRecvBytes-LastRecvBytes ELSE BaseRecvBytes END,LastSendBytes=?,LastRecvBytes=?,UpdatedDate=? WHERE ServerID=? AND SubjectType=? AND HubName=? AND UserKey=?`, send, recv, send, recv, now(), serverID, subject, hub, fold(name))
}
func (a *App) refreshQuota(ctx context.Context, serverID int64, subject, hub, name string) error {
	method := "GetHubStatus"
	params := M{"HubName_str": hub}
	if subject == "user" {
		method = "GetUser"
		params["Name_str"] = name
	}
	client, err := a.clientFor(serverID)
	if err != nil {
		return err
	}
	out, err := client.Call(ctx, method, params)
	if err != nil {
		return err
	}
	send, recv := bytesFor(out, "")
	a.observeQuotaLocked(serverID, subject, hub, name, send, recv)
	return nil
}
func userWriteBody(row M) M {
	out := M{}
	for k, v := range row {
		if len(k) >= 5 && (k[:5] == "Recv." || k[:5] == "Send.") {
			continue
		}
		if k != "Auth_Password_str" && k != "NumLogin_u32" {
			out[k] = v
		}
	}
	return out
}
func (a *App) releaseQuota(ctx context.Context, row M) {
	if str(row["RestoreState"]) == "" {
		return
	}
	var restore M
	check(json.Unmarshal([]byte(str(row["RestoreState"])), &restore))
	hub, name := str(row["HubName"]), str(row["UserName"])
	if str(row["SubjectType"]) == "hub" {
		if boolean(withDefault(restore, "Online_bool", true)) {
			a.rpc(ctx, "SetHubOnline", M{"HubName_str": hub, "Online_bool": true})
		}
	} else {
		body := userWriteBody(a.rpc(ctx, "GetUser", M{"HubName_str": hub, "Name_str": name}))
		body["HubName_str"], body["Name_str"] = hub, name
		body["UsePolicy_bool"] = boolean(restore["UsePolicy_bool"])
		body["policy:Access_bool"] = boolean(withDefault(restore, "policy:Access_bool", true))
		a.rpc(ctx, "SetUser", body)
	}
	execute(a.db, `UPDATE TrafficQuota SET EnforcedDate=NULL,ExceededDate=NULL,RestoreState='',UpdatedDate=? WHERE TrafficQuotaID=?`, now(), row["TrafficQuotaID"])
	a.audit(nil, "quota.released", str(row["SubjectType"]), hub, name, integer(row["ServerID"]))
}
func (a *App) blockQuota(ctx context.Context, row M) {
	hub, name := str(row["HubName"]), str(row["UserName"])
	method := "GetHub"
	params := M{"HubName_str": hub}
	if str(row["SubjectType"]) == "user" {
		method = "GetUser"
		params["Name_str"] = name
	}
	current := a.rpc(ctx, method, params)
	if str(row["RestoreState"]) == "" {
		restore := M{"Online_bool": boolean(withDefault(current, "Online_bool", true)), "UsePolicy_bool": boolean(current["UsePolicy_bool"]), "policy:Access_bool": boolean(withDefault(current, "policy:Access_bool", true))}
		encoded, err := json.Marshal(restore)
		check(err)
		// Keep the original state even if the remote reply or final local write is lost.
		execute(a.db, `UPDATE TrafficQuota SET RestoreState=?,UpdatedDate=? WHERE TrafficQuotaID=?`, string(encoded), now(), row["TrafficQuotaID"])
	}
	if str(row["SubjectType"]) == "hub" {
		if boolean(current["Online_bool"]) {
			a.rpc(ctx, "SetHubOnline", M{"HubName_str": hub, "Online_bool": false})
		}
	} else if !boolean(current["UsePolicy_bool"]) || boolean(current["policy:Access_bool"]) {
		body := userWriteBody(current)
		body["HubName_str"], body["Name_str"], body["UsePolicy_bool"], body["policy:Access_bool"] = hub, name, true, false
		a.rpc(ctx, "SetUser", body)
	}
	execute(a.db, `UPDATE TrafficQuota SET EnforcedDate=COALESCE(EnforcedDate,?),ExceededDate=COALESCE(ExceededDate,?),UpdatedDate=? WHERE TrafficQuotaID=?`, now(), now(), now(), row["TrafficQuotaID"])
	if str(row["EnforcedDate"]) == "" {
		a.audit(nil, "quota.blocked", str(row["SubjectType"]), hub, name, integer(row["ServerID"]))
	}
}
func (a *App) cutQuotaSessions(ctx context.Context, row M) {
	hub, name := str(row["HubName"]), str(row["UserName"])
	for _, s := range list(a.rpc(ctx, "EnumSession", M{"HubName_str": hub})["SessionList"]) {
		if fold(str(s["Username_str"])) == fold(name) {
			_, err := a.client(ctx).Call(ctx, "DeleteSession", M{"HubName_str": hub, "Name_str": s["Name_str"]})
			if err != nil {
				log.Printf("quota disconnect: %v", err)
			}
		}
	}
}
func (a *App) quotaOver(row M) bool {
	out := quotaPublic(row)
	return boolean(a.setting("quota_enforcement_enabled")) && boolean(out["enabled"]) && integer(out["limit_bytes"]) > 0 && integer(out["used_bytes"]) >= integer(out["limit_bytes"])
}
func (a *App) guardHubOnline(ctx context.Context, method string, params M) {
	if (method == "SetHub" || method == "SetHubOnline") && boolean(params["Online_bool"]) && a.quotaOver(a.quotaRow(a.serverIDFromContext(ctx), "hub", str(params["HubName_str"]), "")) {
		fail(409, "This hub is over its traffic limit. Raise or reset the limit before bringing it online.")
	}
}
func (a *App) enforceQuota(ctx context.Context, row M) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("quota enforcement: %v", p)
		}
	}()
	if a.quotaOver(row) {
		a.blockQuota(ctx, row)
		if str(row["SubjectType"]) == "user" {
			a.cutQuotaSessions(ctx, row)
		}
	} else if str(row["RestoreState"]) != "" {
		a.releaseQuota(ctx, row)
	}
}
func (a *App) quotaTick(ctx context.Context) {
	var jobs sync.WaitGroup
	for _, server := range query(a.db, `SELECT DISTINCT s.ServerID FROM Server s JOIN TrafficQuota q ON q.ServerID=s.ServerID WHERE s.IsEnabled=1`) {
		id := integer(server["ServerID"])
		jobs.Add(1)
		go func() {
			defer jobs.Done()
			defer func() {
				if p := recover(); p != nil {
					log.Printf("quota server %d: %v", id, p)
				}
			}()
			lock := a.quotaLock(id)
			lock.Lock()
			defer lock.Unlock()
			serverCtx := context.WithValue(ctx, serverIDKey, id)
			for _, row := range query(a.db, `SELECT * FROM TrafficQuota WHERE ServerID=?`, id) {
				subject, hub, name := str(row["SubjectType"]), str(row["HubName"]), str(row["UserName"])
				if err := a.refreshQuota(serverCtx, id, subject, hub, name); err != nil {
					log.Printf("quota reading %d/%s/%s: %v", id, hub, name, err)
					if e, ok := err.(httpError); !ok || e.Status >= 500 || e.Status == 409 {
						break
					}
					continue
				}
				a.enforceQuota(serverCtx, a.quotaRow(id, subject, hub, name))
			}
		}()
	}
	jobs.Wait()
}
func (a *App) registerQuotas() {
	a.endpoint("GET /quotas", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		out := []M{}
		serverID := a.serverIDFromContext(r.Context())
		for _, row := range query(a.db, `SELECT * FROM TrafficQuota WHERE ServerID=? ORDER BY HubName,SubjectType,UserName`, serverID) {
			out = append(out, quotaPublic(row))
		}
		return out
	})
	for _, subject := range []string{"hub", "user"} {
		base := "/quotas/" + subject + "/{hub}"
		if subject == "user" {
			base += "/{name}"
		}
		for _, action := range []string{"get", "set", "enabled", "reset", "delete"} {
			pattern := "GET " + base
			switch action {
			case "set":
				pattern = "PUT " + base
			case "enabled":
				pattern = "PUT " + base + "/enabled"
			case "reset":
				pattern = "POST " + base + "/reset"
			case "delete":
				pattern = "DELETE " + base
			}
			a.endpoint(pattern, func(w http.ResponseWriter, r *http.Request, u M) any {
				serverID := a.serverIDFromContext(r.Context())
				lock := a.quotaLock(serverID)
				lock.Lock()
				defer lock.Unlock()
				hub, name := r.PathValue("hub"), r.PathValue("name")
				row := a.quotaRow(serverID, subject, hub, name)
				if action == "set" {
					b := readBody(w, r)
					amount := number(b["limit"])
					if _, ok := b["limit"]; !ok || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > 1e6 {
						fail(422, "Limit must be between 0 and 1000000.")
					}
					unit := str(withDefault(b, "unit", "GB"))
					size := map[string]float64{"MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}[unit]
					if size == 0 {
						fail(422, "Unit must be MB, GB or TB.")
					}
					a.saveQuota(serverID, subject, hub, name, int64(math.Round(amount*size)), str(withDefault(b, "metric", "total")), boolean(withDefault(b, "enabled", true)))
				} else if action == "enabled" {
					if row == nil || integer(row["LimitBytes"]) == 0 {
						fail(409, "No traffic limit is set on this subject.")
					}
					b := readBody(w, r)
					on, ok := b["enabled"].(bool)
					if !ok {
						fail(422, "enabled must be a boolean.")
					}
					execute(a.db, `UPDATE TrafficQuota SET IsEnabled=?,UpdatedDate=? WHERE TrafficQuotaID=?`, on, now(), row["TrafficQuotaID"])
				} else if action == "delete" {
					if row == nil {
						fail(404, "No traffic limit is set on this subject.")
					}
					a.releaseQuota(r.Context(), row)
					if integer(row["BaseSendBytes"]) != 0 || integer(row["BaseRecvBytes"]) != 0 {
						execute(a.db, `UPDATE TrafficQuota SET LimitBytes=0,ExceededDate=NULL,UpdatedDate=? WHERE TrafficQuotaID=?`, now(), row["TrafficQuotaID"])
					} else {
						execute(a.db, `DELETE FROM TrafficQuota WHERE TrafficQuotaID=?`, row["TrafficQuotaID"])
					}
					a.audit(u, "quota.removed", subject, hub, name)
					return M{"ok": true}
				} else if action == "reset" {
					if row == nil {
						a.saveQuota(serverID, subject, hub, name, 0, "total", true)
					}
					check(a.refreshQuota(r.Context(), serverID, subject, hub, name))
					row = a.quotaRow(serverID, subject, hub, name)
					a.releaseQuota(r.Context(), row)
					execute(a.db, `UPDATE TrafficQuota SET BaseSendBytes=LastSendBytes,BaseRecvBytes=LastRecvBytes,CycleStartDate=?,ExceededDate=NULL,UpdatedDate=? WHERE TrafficQuotaID=?`, now(), now(), row["TrafficQuotaID"])
				} else if row == nil {
					fail(404, "No traffic limit is set on this subject.")
				}
				check(a.refreshQuota(r.Context(), serverID, subject, hub, name))
				if action != "get" {
					a.enforceQuota(r.Context(), a.quotaRow(serverID, subject, hub, name))
					a.audit(u, "quota."+action, subject, hub, name)
				}
				return quotaPublic(a.quotaRow(serverID, subject, hub, name))
			})
		}
	}
}
