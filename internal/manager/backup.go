package manager

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupInfo represents a saved SoftEther configuration backup file.
type BackupInfo struct {
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"created_at"`
	IsAuto    bool   `json:"is_auto"`
}

func (a *App) getBackupDir(serverID int64) string {
	dir := strings.TrimSpace(str(a.setting("backup_dir")))
	if dir == "" {
		dir = filepath.Join(a.cfg.DataDir, "backups")
	}
	if serverID > 1 {
		dir = filepath.Join(dir, fmt.Sprintf("server_%d", serverID))
	}
	_ = os.MkdirAll(dir, 0755)
	return dir
}

// rotateAutoBackups keeps only the newest maxKeep automatic backup files.
func rotateAutoBackups(dir string, maxKeep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var autoFiles []string
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.Contains(name, "_auto_") && strings.HasSuffix(name, ".config") {
			autoFiles = append(autoFiles, name)
		}
	}
	// Sort descending so the most recent files are first
	sort.Sort(sort.Reverse(sort.StringSlice(autoFiles)))
	if len(autoFiles) > maxKeep {
		for _, name := range autoFiles[maxKeep:] {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
	return nil
}

// createServerBackup fetches current vpn_server.config and saves it to disk.
func (a *App) createServerBackup(ctx context.Context, serverID int64, isAuto bool) (*BackupInfo, error) {
	sCtx := context.WithValue(ctx, serverIDKey, serverID)
	out := a.rpc(sCtx, "GetConfig", nil)
	rawB64 := str(out["FileData_bin"])
	if rawB64 == "" {
		return nil, fmt.Errorf("empty configuration received from server")
	}
	data, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode configuration base64: %w", err)
	}

	dir := a.getBackupDir(serverID)
	ts := time.Now().Format("20060102_150405")
	kind := "manual"
	if isAuto {
		kind = "auto"
	}

	var filename string
	if serverID > 1 {
		filename = fmt.Sprintf("vpn_server_s%d_%s_%s.config", serverID, kind, ts)
	} else {
		filename = fmt.Sprintf("vpn_server_%s_%s.config", kind, ts)
	}

	targetPath := filepath.Join(dir, filename)
	if err := os.WriteFile(targetPath, data, 0644); err != nil {
		return nil, fmt.Errorf("failed to write backup file: %w", err)
	}

	if isAuto {
		_ = rotateAutoBackups(dir, 15)
	}

	return &BackupInfo{
		Filename:  filename,
		Size:      int64(len(data)),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		IsAuto:    isAuto,
	}, nil
}

// listBackups returns all configuration backup files for the specified server.
func (a *App) listBackups(serverID int64) []BackupInfo {
	dir := a.getBackupDir(serverID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []BackupInfo{}
	}
	out := []BackupInfo{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "vpn_server_") || !strings.HasSuffix(name, ".config") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		isAuto := strings.Contains(name, "_auto_")
		out = append(out, BackupInfo{
			Filename:  name,
			Size:      info.Size(),
			CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
			IsAuto:    isAuto,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Filename > out[j].Filename
	})
	return out
}

func isSafeBackupFilename(name string) bool {
	if name == "" || filepath.Base(name) != name {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	return strings.HasPrefix(name, "vpn_server_") && strings.HasSuffix(name, ".config")
}

func shouldRunBackup(enabled bool, intervalHours int64, backupTime, lastRunStr string, now time.Time) bool {
	if !enabled {
		return false
	}
	if intervalHours <= 0 {
		intervalHours = 24
	}
	var lastRun time.Time
	if lastRunStr != "" {
		if t, err := time.Parse(time.RFC3339, lastRunStr); err == nil {
			lastRun = t
		}
	}

	if intervalHours >= 24 && backupTime != "" {
		var h, m int
		_, _ = fmt.Sscanf(backupTime, "%d:%d", &h, &m)
		targetToday := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if now.Before(targetToday) {
			yesterdayTarget := targetToday.Add(-24 * time.Hour)
			if lastRun.IsZero() || lastRun.Before(yesterdayTarget) {
				return true
			}
			return false
		}
		if lastRun.IsZero() || lastRun.Before(targetToday) {
			return true
		}
		return false
	}

	if lastRun.IsZero() {
		return true
	}
	return now.Sub(lastRun) >= time.Duration(intervalHours)*time.Hour
}

func (a *App) backupTick(ctx context.Context) {
	enabled := boolean(a.setting("backup_enabled"))
	intervalHours := integer(a.setting("backup_interval_hours"))
	backupTime := str(a.setting("backup_time"))
	lastRunStr := str(a.setting("backup_last_run"))

	if !shouldRunBackup(enabled, intervalHours, backupTime, lastRunStr, time.Now()) {
		return
	}

	servers := query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`)
	if len(servers) == 0 {
		return
	}

	ranAny := false
	for _, s := range servers {
		serverID := integer(s["ServerID"])
		func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("auto backup failed for server %d: %v", serverID, p)
				}
			}()
			info, err := a.createServerBackup(ctx, serverID, true)
			if err != nil {
				log.Printf("auto backup error for server %d: %v", serverID, err)
			} else {
				ranAny = true
				log.Printf("auto backup created for server %d: %s (%d bytes)", serverID, info.Filename, info.Size)
			}
		}()
	}

	if ranAny {
		a.set("backup_last_run", time.Now().UTC().Format(time.RFC3339))
	}
}

func (a *App) registerBackups() {
	a.endpoint("GET /server/backups", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		return M{
			"backups": a.listBackups(serverID),
		}
	})

	a.endpoint("POST /server/backups", func(_ http.ResponseWriter, r *http.Request, u M) any {
		serverID := a.serverIDFromContext(r.Context())
		info, err := a.createServerBackup(r.Context(), serverID, false)
		if err != nil {
			fail(500, "Failed to create backup: "+err.Error())
		}
		a.audit(u, "server.config_backed_up", "server", info.Filename, fmt.Sprintf("%d bytes", info.Size))
		return info
	})

	a.endpoint("GET /server/backups/{filename}/download", func(w http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		filename := r.PathValue("filename")
		if !isSafeBackupFilename(filename) {
			fail(400, "Invalid backup filename.")
		}
		dir := a.getBackupDir(serverID)
		filePath := filepath.Join(dir, filename)
		f, err := os.Open(filePath)
		if err != nil {
			fail(404, "Backup file not found.")
		}
		defer f.Close()

		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.Copy(w, f)
		return nil
	})

	a.endpoint("POST /server/backups/{filename}/restore", func(_ http.ResponseWriter, r *http.Request, u M) any {
		serverID := a.serverIDFromContext(r.Context())
		filename := r.PathValue("filename")
		if !isSafeBackupFilename(filename) {
			fail(400, "Invalid backup filename.")
		}
		dir := a.getBackupDir(serverID)
		filePath := filepath.Join(dir, filename)
		data, err := os.ReadFile(filePath)
		if err != nil {
			fail(404, "Backup file not found.")
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		a.rpc(r.Context(), "SetConfig", M{"FileData_bin": b64})
		a.audit(u, "config.restored_from_backup", "server", filename, fmt.Sprintf("%d bytes", len(data)))
		return M{"ok": true, "filename": filename}
	})

	a.endpoint("DELETE /server/backups/{filename}", func(_ http.ResponseWriter, r *http.Request, u M) any {
		serverID := a.serverIDFromContext(r.Context())
		filename := r.PathValue("filename")
		if !isSafeBackupFilename(filename) {
			fail(400, "Invalid backup filename.")
		}
		dir := a.getBackupDir(serverID)
		filePath := filepath.Join(dir, filename)
		if err := os.Remove(filePath); err != nil {
			fail(404, "Backup file not found or could not be deleted.")
		}
		a.audit(u, "config.backup_deleted", "server", filename, "")
		return M{"ok": true, "deleted": filename}
	})

	a.endpoint("GET /server/backup-settings", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		serverID := a.serverIDFromContext(r.Context())
		return M{
			"enabled":         boolean(a.setting("backup_enabled")),
			"interval_hours":  integer(a.setting("backup_interval_hours")),
			"backup_time":     str(a.setting("backup_time")),
			"backup_dir":      a.getBackupDir(serverID),
			"configured_dir":  str(a.setting("backup_dir")),
			"retention_count": 15,
			"last_run":        str(a.setting("backup_last_run")),
		}
	})

	a.endpoint("PUT /server/backup-settings", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		if enabled, ok := b["enabled"].(bool); ok {
			a.set("backup_enabled", enabled)
		}
		if v, exists := b["interval_hours"]; exists {
			if interval := integer(v); interval >= 1 {
				a.set("backup_interval_hours", int(interval))
			}
		}
		if t, ok := b["backup_time"].(string); ok && t != "" {
			a.set("backup_time", t)
		}
		if d, ok := b["backup_dir"].(string); ok {
			a.set("backup_dir", strings.TrimSpace(d))
		}
		serverID := a.serverIDFromContext(r.Context())
		a.audit(u, "server.backup_settings_updated", "server", "", "")
		return M{
			"enabled":         boolean(a.setting("backup_enabled")),
			"interval_hours":  integer(a.setting("backup_interval_hours")),
			"backup_time":     str(a.setting("backup_time")),
			"backup_dir":      a.getBackupDir(serverID),
			"configured_dir":  str(a.setting("backup_dir")),
			"retention_count": 15,
			"last_run":        str(a.setting("backup_last_run")),
		}
	})
}

func isIP(s string) bool {
	return net.ParseIP(strings.TrimSpace(s)) != nil
}

// enrichSessions supplements session entries with Virtual IP and clean hostname.
func (a *App) enrichSessions(ctx context.Context, hub string, sessions []M) []M {
	if len(sessions) == 0 {
		return sessions
	}
	var ipTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumIpTable", M{"HubName_str": hub})
		ipTable = list(out["IpTable"])
	}()

	var dhcpTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumDHCP", M{"HubName_str": hub})
		dhcpTable = list(out["DhcpTable"])
	}()

	sessionIPs := make(map[string][]string)
	sessionRemoteHosts := make(map[string]string)
	for _, entry := range ipTable {
		sessName := str(entry["SessionName_str"])
		ip := str(entry["IpAddress_ip"])
		if sessName != "" && ip != "" {
			found := false
			for _, existing := range sessionIPs[sessName] {
				if existing == ip {
					found = true
					break
				}
			}
			if !found {
				sessionIPs[sessName] = append(sessionIPs[sessName], ip)
			}
		}
		rHost := str(entry["RemoteHostname_str"])
		if rHost != "" && !isIP(rHost) && sessionRemoteHosts[sessName] == "" {
			sessionRemoteHosts[sessName] = rHost
		}
	}

	ipToDhcpHost := make(map[string]string)
	for _, entry := range dhcpTable {
		ip := str(entry["IpAddress_ip"])
		h := str(entry["Hostname_str"])
		if ip != "" && h != "" && !isIP(h) {
			ipToDhcpHost[ip] = h
		}
	}

	for _, s := range sessions {
		sessName := str(s["Name_str"])
		clientIP := str(s["ClientIP_ip"])
		host := str(s["Hostname_str"])

		if ips, ok := sessionIPs[sessName]; ok && len(ips) > 0 {
			s["VirtualIP_ip"] = strings.Join(ips, ", ")
		} else {
			s["VirtualIP_ip"] = ""
		}

		var realHost string
		if sessionRemoteHosts[sessName] != "" {
			realHost = sessionRemoteHosts[sessName]
		} else if ips, ok := sessionIPs[sessName]; ok {
			for _, ip := range ips {
				if dhcpH := ipToDhcpHost[ip]; dhcpH != "" {
					realHost = dhcpH
					break
				}
			}
		}

		if host != "" && host != clientIP && !isIP(host) {
			realHost = host
		}

		s["RealHostname_str"] = realHost
		if isIP(host) || host == clientIP {
			s["Hostname_str"] = realHost
		}
	}
	return sessions
}

// enrichSessionStatus supplements single session status with Virtual IP and clean hostname.
func (a *App) enrichSessionStatus(ctx context.Context, hub, name string, status M) M {
	if status == nil {
		return status
	}
	clientIP := str(status["Client_Ip_Address_ip"])
	clientHost := str(status["SessionStatus_ClientHostName_str"])
	if clientHost == "" {
		clientHost = str(status["ClientHostname_str"])
	}

	var ipTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumIpTable", M{"HubName_str": hub})
		ipTable = list(out["IpTable"])
	}()

	var dhcpTable []M
	func() {
		defer func() { _ = recover() }()
		out := a.rpc(ctx, "EnumDHCP", M{"HubName_str": hub})
		dhcpTable = list(out["DhcpTable"])
	}()

	var virtualIPs []string
	var remoteHost string
	for _, entry := range ipTable {
		if str(entry["SessionName_str"]) == name {
			ip := str(entry["IpAddress_ip"])
			if ip != "" {
				found := false
				for _, ex := range virtualIPs {
					if ex == ip {
						found = true
						break
					}
				}
				if !found {
					virtualIPs = append(virtualIPs, ip)
				}
			}
			rH := str(entry["RemoteHostname_str"])
			if rH != "" && !isIP(rH) && remoteHost == "" {
				remoteHost = rH
			}
		}
	}

	var dhcpHost string
	for _, ip := range virtualIPs {
		for _, entry := range dhcpTable {
			if str(entry["IpAddress_ip"]) == ip {
				h := str(entry["Hostname_str"])
				if h != "" && !isIP(h) {
					dhcpHost = h
					break
				}
			}
		}
		if dhcpHost != "" {
			break
		}
	}

	if len(virtualIPs) > 0 {
		status["VirtualIP_ip"] = strings.Join(virtualIPs, ", ")
	} else {
		status["VirtualIP_ip"] = ""
	}

	var realHost string
	if remoteHost != "" {
		realHost = remoteHost
	} else if dhcpHost != "" {
		realHost = dhcpHost
	} else if clientHost != "" && clientHost != clientIP && !isIP(clientHost) {
		realHost = clientHost
	}

	status["RealHostname_str"] = realHost
	if isIP(clientHost) || clientHost == clientIP {
		status["SessionStatus_ClientHostName_str"] = realHost
		status["ClientHostname_str"] = realHost
	}
	return status
}
