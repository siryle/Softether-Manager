package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type updateState struct {
	mu       sync.Mutex
	applyMu  sync.Mutex
	checking bool
	checked  time.Time
	latest   M
	error    string
}

func command(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return string(b), fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
func (a *App) serviceStatus() M {
	out := M{"managed": false, "active": "", "sub": "", "since": "", "unit": a.cfg.ServiceName}
	_, err := os.Stat("/run/systemd/system")
	if err != nil {
		return out
	}
	text, err := command(context.Background(), "systemctl", "show", a.cfg.ServiceName, "--property=ActiveState", "--property=SubState", "--property=ActiveEnterTimestamp")
	if err == nil {
		for _, line := range strings.Split(text, "\n") {
			key, value, _ := strings.Cut(line, "=")
			switch key {
			case "ActiveState":
				out["active"], out["managed"] = value, value != ""
			case "SubState":
				out["sub"] = value
			case "ActiveEnterTimestamp":
				out["since"] = value
			}
		}
	}
	return out
}
func (a *App) updateUnavailable() string {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return "This panel is not running under systemd. Update it the way it was installed."
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return "systemd-run is required to update outside the panel service."
	}
	if os.Geteuid() != 0 {
		return "The panel must run as root to install updates."
	}
	if _, err := os.Stat(env("SEM_CLI_PATH", "/usr/local/bin/sem")); err != nil {
		return "The sem command-line tool is not installed."
	}
	return ""
}
func isNewer(current, candidate string) bool {
	version := regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)
	a, b := version.FindStringSubmatch(current), version.FindStringSubmatch(candidate)
	if a == nil || b == nil || (integer(a[1])+integer(a[2])+integer(a[3]) == 0) {
		return false
	}
	for i := 1; i <= 3; i++ {
		if integer(a[i]) != integer(b[i]) {
			return integer(a[i]) < integer(b[i])
		}
	}
	return false
}
func (a *App) fetchUpdate(ctx context.Context) {
	defer func() {
		a.updates.mu.Lock()
		a.updates.checking = false
		a.updates.checked = time.Now()
		a.updates.mu.Unlock()
	}()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+a.cfg.Repo+"/releases/latest", nil)
	if err == nil {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "SoftEther-Manager/"+a.cfg.Version)
	}
	var release M
	if err == nil {
		res, e := (&http.Client{Timeout: 15 * time.Second}).Do(req)
		err = e
		if e == nil {
			defer res.Body.Close()
			if res.StatusCode != 200 {
				err = fmt.Errorf("release host returned HTTP %d", res.StatusCode)
				if res.StatusCode == 404 {
					if b, e := command(ctx, "gh", "api", "repos/"+a.cfg.Repo+"/releases/latest"); e == nil {
						err = json.Unmarshal([]byte(b), &release)
					}
				}
			} else {
				err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&release)
			}
		}
	}
	a.updates.mu.Lock()
	defer a.updates.mu.Unlock()
	if err != nil {
		a.updates.error = err.Error()
		return
	}
	if str(release["tag_name"]) == "" {
		a.updates.error = "The release host named no version."
		return
	}
	notes := str(release["body"])
	if len(notes) > 8000 {
		notes = notes[:8000] + "\n..."
	}
	a.updates.latest = M{"version": release["tag_name"], "name": release["name"], "url": release["html_url"], "published_at": release["published_at"], "notes": notes}
	a.updates.error = ""
}
func (a *App) updateStatus(ctx context.Context, refresh bool) M {
	enabled := boolean(a.setting("update_check_enabled"))
	interval := time.Duration(max(int64(1), integer(a.setting("update_check_interval_hours")))) * time.Hour
	a.updates.mu.Lock()
	fetch := !a.updates.checking && (refresh || (enabled && time.Since(a.updates.checked) >= interval))
	if fetch {
		a.updates.checking = true
	}
	a.updates.mu.Unlock()
	if fetch {
		if refresh {
			a.fetchUpdate(ctx)
		} else {
			a.workers.Add(1)
			go func() {
				defer a.workers.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				a.fetchUpdate(ctx)
			}()
		}
	}
	a.updates.mu.Lock()
	defer a.updates.mu.Unlock()
	latest := clone(a.updates.latest)
	for _, key := range []string{"version", "name", "url", "published_at", "notes"} {
		if latest[key] == nil {
			latest[key] = ""
		}
	}
	checked := ""
	if !a.updates.checked.IsZero() {
		checked = a.updates.checked.UTC().Format(time.RFC3339)
	}
	reason := a.updateUnavailable()
	return M{"current_version": a.cfg.Version, "latest": latest, "update_available": isNewer(a.cfg.Version, str(latest["version"])), "checked_at": checked, "checking": a.updates.checking, "error": a.updates.error, "note": "", "source": a.cfg.Repo, "enabled": enabled, "can_apply": reason == "", "unavailable_reason": reason}
}
func (a *App) readUpdateRun() M {
	b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "update-state.json"))
	out := M{"stage": "idle"}
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}
func (a *App) writeUpdateRun(state M) {
	b, err := json.Marshal(state)
	check(err)
	check(atomicWrite(filepath.Join(a.cfg.DataDir, "update-state.json"), b, 0600))
}
func (a *App) updateRunState() M {
	a.updates.applyMu.Lock()
	defer a.updates.applyMu.Unlock()
	state := a.readUpdateRun()
	if str(state["stage"]) == "running" {
		out, err := command(context.Background(), "systemctl", "show", "softether-manager-update", "--property=ActiveState,Result,ExecMainStatus,SubState")
		fields := M{}
		for _, line := range strings.Split(out, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				fields[k] = v
			}
		}
		finished := false
		if str(fields["Result"]) != "" && str(fields["Result"]) != "success" {
			state["stage"], state["error"] = "failed", fields["Result"]
			finished = true
		} else if str(fields["SubState"]) == "exited" {
			state["stage"] = "succeeded"
			if str(fields["ExecMainStatus"]) != "0" {
				state["stage"], state["error"] = "failed", "Installer exited nonzero."
			}
			finished = true
		} else if err != nil || time.Since(parseTime(str(state["started_at"]))) > 30*time.Minute {
			if a.cfg.Version != str(state["from_version"]) {
				state["stage"] = "succeeded"
			} else {
				state["stage"], state["error"] = "failed", "Update no longer running or timed out; inspect the service log."
			}
			finished = true
		}
		if finished {
			state["finished_at"] = now()
			a.writeUpdateRun(state)
		}
	}
	lines := []string{}
	if b, err := os.ReadFile(filepath.Join(a.cfg.DataDir, "update.log")); err == nil {
		lines = strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
	}
	state["log"] = lines
	return state
}
func (a *App) applyUpdate(ctx context.Context, version string, user M) M {
	if reason := a.updateUnavailable(); reason != "" {
		fail(409, reason)
	}
	if version == "" {
		version = "latest"
	}
	if version != "latest" && !regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+][a-zA-Z0-9.-]+)?$`).MatchString(version) {
		fail(422, "Invalid release version.")
	}
	a.updates.applyMu.Lock()
	defer a.updates.applyMu.Unlock()
	if str(a.readUpdateRun()["stage"]) == "running" {
		fail(409, "An update is already running.")
	}
	state := M{"stage": "running", "target_version": version, "from_version": a.cfg.Version, "started_at": now(), "finished_at": "", "error": "", "unit": "softether-manager-update", "started_by": user["Username"]}
	a.writeUpdateRun(state)
	logPath := filepath.Join(a.cfg.DataDir, "update.log")
	check(atomicWrite(logPath, nil, 0600))
	_, _ = command(ctx, "systemctl", "stop", "softether-manager-update")
	_, _ = command(ctx, "systemctl", "reset-failed", "softether-manager-update")
	_, err := command(ctx, "systemd-run", "--unit=softether-manager-update", "--description=SoftEther Manager update", "--remain-after-exit", "--property=StandardOutput=append:"+logPath, "--property=StandardError=append:"+logPath, env("SEM_CLI_PATH", "/usr/local/bin/sem"), "update", "--yes", "--version", version)
	if err != nil {
		state["stage"], state["finished_at"], state["error"] = "failed", now(), err.Error()
		a.writeUpdateRun(state)
		fail(502, err.Error())
	}
	a.audit(user, "panel.update_started", "panel", version, "")
	state["log"] = []string{}
	return state
}
func (a *App) restart(ctx context.Context) {
	if reason := a.updateUnavailable(); reason != "" {
		fail(409, reason)
	}
	_, err := command(ctx, "systemd-run", "--unit=softether-manager-restart", "--on-active=2s", "systemctl", "restart", a.cfg.ServiceName)
	check(err)
}
