package manager

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

func (a *App) backupRoot() string {
	dir := strings.TrimSpace(str(a.setting("backup_dir")))
	if dir == "" {
		dir = filepath.Join(a.cfg.DataDir, "backups")
	}
	return dir
}

func (a *App) getBackupDir(serverID int64) string {
	dir := a.backupRoot()
	if serverID > 1 {
		dir = filepath.Join(dir, fmt.Sprintf("server_%d", serverID))
	}
	_ = os.MkdirAll(dir, 0700)
	return dir
}

func (a *App) protectExistingBackups() error {
	root := a.backupRoot()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	dirs := []string{root}
	for _, entry := range entries {
		if id, ok := strings.CutPrefix(entry.Name(), "server_"); ok && entry.IsDir() {
			if _, err := strconv.ParseUint(id, 10, 64); err == nil {
				dirs = append(dirs, filepath.Join(root, entry.Name()))
			}
		}
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && isSafeBackupFilename(entry.Name()) {
				if err := os.Chmod(filepath.Join(dir, entry.Name()), 0600); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
func (a *App) backupRetentionCount() int {
	n := int(integer(a.setting("backup_retention_count")))
	if n < 1 {
		return 15
	}
	if n > 100 {
		return 100
	}
	return n
}

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
	ts := time.Now().UTC().Format("20060102_150405.000000000")
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
	if err := atomicWrite(targetPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write backup file: %w", err)
	}

	if isAuto {
		_ = rotateAutoBackups(dir, a.backupRetentionCount())
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

	if intervalHours == 24 && backupTime != "" {
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
	if !boolean(a.setting("backup_enabled")) {
		return
	}
	interval := integer(a.setting("backup_interval_hours"))
	target := str(a.setting("backup_time"))
	for _, s := range query(a.db, `SELECT ServerID FROM Server WHERE IsEnabled=1`) {
		if ctx.Err() != nil {
			return
		}
		id := integer(s["ServerID"])
		row := one(a.db, `SELECT BackupLastRun FROM Server WHERE ServerID=? AND IsEnabled=1`, id)
		if row == nil || !shouldRunBackup(true, interval, target, str(row["BackupLastRun"]), time.Now()) {
			continue
		}
		err := recoverAsError(func() error {
			_, err := a.createServerBackup(ctx, id, true)
			return err
		})
		if err != nil {
			log.Printf("auto backup failed for server %d: %v", id, err)
			execute(a.db, `UPDATE Server SET BackupLastError=? WHERE ServerID=?`, err.Error(), id)
			continue
		}
		execute(a.db, `UPDATE Server SET BackupLastRun=?,BackupLastError='' WHERE ServerID=?`, now(), id)
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
		w.Header().Set("Cache-Control", "no-store")
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
		out := a.rpc(r.Context(), "SetConfig", M{"FileData_bin": b64})
		a.audit(u, "config.restored_from_backup", "server", filename, fmt.Sprintf("%d bytes", len(data)))
		return M{"ok": true, "filename": filename, "restarting": out["restarting"]}
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

	settings := func(serverID int64) M {
		row := a.serverRow(serverID)
		return M{
			"enabled":         boolean(a.setting("backup_enabled")),
			"interval_hours":  integer(a.setting("backup_interval_hours")),
			"backup_time":     str(a.setting("backup_time")),
			"backup_dir":      a.getBackupDir(serverID),
			"configured_dir":  str(a.setting("backup_dir")),
			"retention_count": a.backupRetentionCount(),
			"last_run":        row["BackupLastRun"],
			"last_error":      row["BackupLastError"],
		}
	}
	a.endpoint("GET /server/backup-settings", func(_ http.ResponseWriter, r *http.Request, _ M) any {
		return settings(a.serverIDFromContext(r.Context()))
	})

	a.endpoint("PUT /server/backup-settings", func(w http.ResponseWriter, r *http.Request, u M) any {
		b := readBody(w, r)
		if v, exists := b["interval_hours"]; exists {
			interval := integer(v)
			if number(v) != float64(interval) || (interval != 6 && interval != 12 && interval != 24) {
				fail(422, "Backup interval must be 6, 12 or 24 hours.")
			}
		}
		if enabled, ok := b["enabled"].(bool); ok {
			a.set("backup_enabled", enabled)
		}
		if v, exists := b["interval_hours"]; exists {
			a.set("backup_interval_hours", integer(v))
		}
		if v, exists := b["retention_count"]; exists {
			if keep := integer(v); keep >= 1 && keep <= 100 {
				a.set("backup_retention_count", int(keep))
			}
		}
		if t, ok := b["backup_time"].(string); ok && t != "" {
			a.set("backup_time", t)
		}
		if d, ok := b["backup_dir"].(string); ok {
			a.set("backup_dir", strings.TrimSpace(d))
			check(a.protectExistingBackups())
		}
		a.audit(u, "server.backup_settings_updated", "server", "", "")
		return settings(a.serverIDFromContext(r.Context()))
	})
}
