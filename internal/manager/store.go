package manager

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

//go:embed schema.sql
var schema string

type M = map[string]any

func openDB(path string) (*sql.DB, error) {
	if strings.Contains(path, "://") {
		return nil, fmt.Errorf("unsupported database URL: only SQLite is supported")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		err = f.Chmod(0600)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if err = os.Chmod(path+suffix, 0600); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	dsn := path
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		abs = filepath.ToSlash(abs)
		if !strings.HasPrefix(abs, "/") {
			abs = "/" + abs
		}
		u := url.URL{Scheme: "file", Path: abs}
		dsn = u.String()
	}
	dsn += "?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=secure_delete(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err = initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func tableColumns(tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func initSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Carry the first connection forward from the former multi-server layout if coming from legacy schema.
	columns, err := tableColumns(tx, "Server")
	if err != nil {
		return err
	}
	if len(columns) > 0 && columns["IsDeleted"] {
		if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS Setting(SettingKey TEXT PRIMARY KEY,SettingValue TEXT NOT NULL,UpdatedDate TEXT NOT NULL)`); err != nil {
			return err
		}
		var host, encrypted string
		var port int
		err = tx.QueryRow(`SELECT Host,Port,EncryptedPassword FROM Server WHERE IsDeleted=0 AND NOT EXISTS(SELECT 1 FROM Setting WHERE SettingKey='se_password') ORDER BY ServerID LIMIT 1`).Scan(&host, &port, &encrypted)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			for key, value := range (M{"se_host": host, "se_port": port, "se_password": encrypted}) {
				b, e := json.Marshal(value)
				if e != nil {
					return e
				}
				if _, e = tx.Exec(`INSERT INTO Setting(SettingKey,SettingValue,UpdatedDate) VALUES(?,?,?) ON CONFLICT DO NOTHING`, key, string(b), now()); e != nil {
					return e
				}
			}
		}
	}

	// Python also rebuilt disposable series carrying the old ServerID dimension without HubName.
	for _, table := range []string{"UserTrafficSample", "HubTrafficSample"} {
		cols, e := tableColumns(tx, table)
		if e != nil {
			return e
		}
		if cols["ServerID"] && !cols["HubName"] {
			if _, e = tx.Exec(`DROP TABLE "` + table + `"`); e != nil {
				return e
			}
		}
	}

	// 1. Ensure Server table exists with expected structure
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS "Server" (
		"ServerID"          INTEGER PRIMARY KEY AUTOINCREMENT,
		"Name"              TEXT    NOT NULL DEFAULT 'Default Server',
		"Host"              TEXT    NOT NULL DEFAULT '127.0.0.1',
		"Port"              INTEGER NOT NULL DEFAULT 5555,
		"EncryptedPassword" TEXT    NOT NULL DEFAULT '',
		"AuthType"          TEXT    NOT NULL DEFAULT 'header',
		"SkipCertCheck"     INTEGER NOT NULL DEFAULT 0,
		"IsEnabled"         INTEGER NOT NULL DEFAULT 1,
		"IsDefault"         INTEGER NOT NULL DEFAULT 0,
		"SortOrder"         INTEGER NOT NULL DEFAULT 0,
		"CreatedDate"       TEXT    NOT NULL DEFAULT '',
		"UpdatedDate"       TEXT    NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}

	serverCols, err := tableColumns(tx, "Server")
	if err != nil {
		return err
	}
	for _, col := range []struct{ column, ddl string }{
		{"Name", "TEXT NOT NULL DEFAULT 'Default Server'"},
		{"Host", "TEXT NOT NULL DEFAULT '127.0.0.1'"},
		{"Port", "INTEGER NOT NULL DEFAULT 5555"},
		{"EncryptedPassword", "TEXT NOT NULL DEFAULT ''"},
		{"AuthType", "TEXT NOT NULL DEFAULT 'header'"},
		{"SkipCertCheck", "INTEGER NOT NULL DEFAULT 0"},
		{"IsEnabled", "INTEGER NOT NULL DEFAULT 1"},
		{"IsDefault", "INTEGER NOT NULL DEFAULT 0"},
		{"SortOrder", "INTEGER NOT NULL DEFAULT 0"},
		{"CreatedDate", "TEXT NOT NULL DEFAULT ''"},
		{"UpdatedDate", "TEXT NOT NULL DEFAULT ''"},
		{"BackupLastRun", "TEXT NOT NULL DEFAULT ''"},
		{"BackupLastError", "TEXT NOT NULL DEFAULT ''"},
		{"PublicHost", "TEXT NOT NULL DEFAULT ''"},
		{"PublicPort", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if !serverCols[col.column] {
			if _, err = tx.Exec(`ALTER TABLE "Server" ADD COLUMN "` + col.column + `" ` + col.ddl); err != nil {
				return err
			}
		}
	}

	// 2. Ensure Setting table exists
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS Setting(SettingKey TEXT PRIMARY KEY,SettingValue TEXT NOT NULL,UpdatedDate TEXT NOT NULL)`); err != nil {
		return err
	}

	// 3. Ensure a default server exists (migrating from Setting if present)
	var serverCount int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM "Server"`).Scan(&serverCount)
	if serverCount == 0 {
		host := "127.0.0.1"
		port := 5555
		encPwd := ""
		auth := "header"

		var hostVal, portVal, pwdVal, authVal string
		_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='se_host'`).Scan(&hostVal)
		_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='se_port'`).Scan(&portVal)
		_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='se_password'`).Scan(&pwdVal)
		_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='se_auth'`).Scan(&authVal)

		if hostVal != "" {
			_ = json.Unmarshal([]byte(hostVal), &host)
		}
		if portVal != "" {
			_ = json.Unmarshal([]byte(portVal), &port)
		}
		if pwdVal != "" {
			_ = json.Unmarshal([]byte(pwdVal), &encPwd)
		}
		if authVal != "" {
			_ = json.Unmarshal([]byte(authVal), &auth)
		}

		stamp := now()
		if _, err = tx.Exec(`INSERT INTO "Server"(Name,Host,Port,EncryptedPassword,AuthType,IsEnabled,IsDefault,SortOrder,CreatedDate,UpdatedDate) VALUES(?,?,?,?,?,1,1,0,?,?)`, "Default Server", host, port, encPwd, auth, stamp, stamp); err != nil {
			return err
		}
	}

	// Preserve the selected server on reopen; legacy layouts may not have a default yet.
	if serverCols["IsDeleted"] {
		if _, err = tx.Exec(`UPDATE Server SET IsEnabled=0,IsDefault=0 WHERE IsDeleted!=0`); err != nil {
			return err
		}
	}
	var defaultID int64
	err = tx.QueryRow(`SELECT ServerID FROM Server WHERE IsEnabled=1 ORDER BY IsDefault DESC,SortOrder,ServerID LIMIT 1`).Scan(&defaultID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE Server SET IsDefault=CASE WHEN ServerID=? THEN 1 ELSE 0 END`, defaultID); err != nil {
		return err
	}

	// Migrate legacy global vpn_public_host/port to the default server if not yet populated.
	var pubHostRaw, pubPortRaw string
	_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='vpn_public_host'`).Scan(&pubHostRaw)
	_ = tx.QueryRow(`SELECT SettingValue FROM Setting WHERE SettingKey='vpn_public_port'`).Scan(&pubPortRaw)
	var legacyPubHost string
	var legacyPubPort int
	if pubHostRaw != "" {
		_ = json.Unmarshal([]byte(pubHostRaw), &legacyPubHost)
	}
	if pubPortRaw != "" {
		_ = json.Unmarshal([]byte(pubPortRaw), &legacyPubPort)
	}
	if legacyPubHost != "" || legacyPubPort > 0 {
		if _, err = tx.Exec(`UPDATE Server SET PublicHost=CASE WHEN PublicHost='' THEN ? ELSE PublicHost END,PublicPort=CASE WHEN PublicPort=0 THEN ? ELSE PublicPort END WHERE ServerID=?`, legacyPubHost, legacyPubPort, defaultID); err != nil {
			return err
		}
	}

	// 4. Ensure all existing tables have ServerID and required migration columns
	for _, migration := range []struct{ table, column, ddl string }{
		{"PanelUser", "SessionVersion", `INTEGER NOT NULL DEFAULT 1`},
		{"UserTrafficSample", "ServerID", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultID)},
		{"HubTrafficSample", "ServerID", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultID)},
		{"VpnSessionSample", "ServerID", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultID)},
		{"VpnSessionSample", "DownloadBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"VpnSessionSample", "UploadBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"VpnSessionSample", "VirtualIP", `TEXT NOT NULL DEFAULT ''`},
		{"VpnSessionTrafficSample", "TotalBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"VpnUserCredential", "ServerID", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultID)},
		{"TrafficQuota", "ServerID", fmt.Sprintf("INTEGER NOT NULL DEFAULT %d", defaultID)},
		{"TrafficQuota", "BaseSendBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"TrafficQuota", "BaseRecvBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"WireGuardPeer", "EncryptedPrivateKey", `TEXT NOT NULL DEFAULT ''`},
		{"AuditLog", "ServerID", `INTEGER DEFAULT NULL`},
	} {
		cols, e := tableColumns(tx, migration.table)
		if e != nil {
			return e
		}
		if len(cols) > 0 && !cols[migration.column] {
			if _, e = tx.Exec(`ALTER TABLE "` + migration.table + `" ADD COLUMN "` + migration.column + `" ` + migration.ddl); e != nil {
				return e
			}
		}
	}

	// SQLite cannot change an inline UNIQUE constraint with ALTER TABLE.
	for _, table := range []string{"VpnSessionSample", "VpnUserCredential", "TrafficQuota"} {
		var ddl string
		err = tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		compact := strings.NewReplacer("\"", "", " ", "", "\n", "", "\t", "", "\r", "").Replace(strings.ToUpper(ddl))
		if !strings.Contains(compact, "UNIQUE(") || strings.Contains(compact, "UNIQUE(SERVERID,") {
			continue
		}
		cols, e := tableColumns(tx, table)
		if e != nil {
			return e
		}
		for _, statement := range strings.Split(schema, ";") {
			if !strings.Contains(statement, `CREATE TABLE IF NOT EXISTS "`+table+`"`) {
				continue
			}
			temp := table + "_multiserver"
			if _, e = tx.Exec(strings.Replace(statement, `"`+table+`"`, `"`+temp+`"`, 1)); e != nil {
				return e
			}
			names := []string{}
			for name := range cols {
				names = append(names, `"`+name+`"`)
			}
			columns := strings.Join(names, ",")
			if _, e = tx.Exec(`INSERT INTO "` + temp + `" (` + columns + `) SELECT ` + columns + ` FROM "` + table + `"`); e != nil {
				return e
			}
			if _, e = tx.Exec(`DROP TABLE "` + table + `"`); e != nil {
				return e
			}
			if _, e = tx.Exec(`ALTER TABLE "` + temp + `" RENAME TO "` + table + `"`); e != nil {
				return e
			}
		}
	}
	for _, name := range []string{"IX_UserTrafficSample_Key", "IX_HubTrafficSample_Key", "IX_VpnSessionSample_Open", "IX_VpnSessionSample_User", "IX_TrafficQuota_Hub"} {
		var ddl string
		e := tx.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&ddl)
		if e == sql.ErrNoRows {
			continue
		}
		if e != nil {
			return e
		}
		if !strings.Contains(strings.ToLower(ddl), "serverid") {
			if _, e = tx.Exec(`DROP INDEX "` + name + `"`); e != nil {
				return e
			}
		}
	}

	// 5. Execute schema to create any missing tables / indexes
	if _, err = tx.Exec(schema); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE TrafficQuota SET LastSendBytes=MAX(0,LastSendBytes),LastRecvBytes=MAX(0,LastRecvBytes) WHERE LastSendBytes<0 OR LastRecvBytes<0`); err != nil {
		return err
	}

	return tx.Commit()
}

func query(db *sql.DB, statement string, args ...any) []M {
	rows, err := db.Query(statement, args...)
	check(err)
	defer rows.Close()
	cols, err := rows.Columns()
	check(err)
	out := []M{}
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		check(rows.Scan(ptrs...))
		row := M{}
		for i, col := range cols {
			if b, ok := values[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = values[i]
			}
		}
		out = append(out, row)
	}
	check(rows.Err())
	return out
}
func one(db *sql.DB, statement string, args ...any) M {
	rows := query(db, statement, args...)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
func execute(db *sql.DB, statement string, args ...any) int64 {
	r, err := db.Exec(statement, args...)
	check(err)
	id, _ := r.LastInsertId()
	return id
}
func (a *App) defaults() M {
	return M{
		"web_path": a.cfg.WebPath, "resource_monitor_enabled": true, "resource_interval_seconds": 3, "resource_history_points": 100,
		"traffic_monitor_enabled": true, "sample_interval_minutes": envInt("SEM_SAMPLE_INTERVAL_MINUTES", 5), "sample_retention_days": envInt("SEM_SAMPLE_RETENTION_DAYS", 90),
		"session_monitor_enabled": true, "session_interval_seconds": 60, "session_traffic_enabled": true, "session_history_retention_days": 30, "quota_enforcement_enabled": true, "quota_interval_seconds": 60,
		"ui_live_seconds": 5, "ui_detail_seconds": 15, "ui_list_seconds": 30, "update_check_enabled": env("SEM_UPDATE_CHECK", "true") != "false", "update_check_interval_hours": envInt("SEM_UPDATE_CHECK_HOURS", 6),
		"se_host": a.cfg.SEHost, "se_port": a.cfg.SEPort, "se_password": "", "se_auth": a.cfg.SEAuth, "se_configured": false,
		"vpn_template": vpnDefaults, "vpn_account_name_template": "{hub} - {username}", "vpn_filename_template": "{hub}-{username}", "vpn_embed_password_default": true,
		"vpn_public_host": "", "vpn_public_port": 443, "wireguard_ports": M{}, "openvpn_ports": M{},
		"domain": "", "https_port": 443, "acme_email": "", "acme_staging": false, "domain_only": false,
		"acme_challenge_type": "dns-01", "cf_dns_token": "", "cert_key_type": "rsa2048", "cert_auto_push": true, "cert_push_server_id": 0, "cert_renew_hook": "",
		"cert_ssh_sync": false, "cert_ssh_host": "", "cert_ssh_port": 22, "cert_ssh_user": "root", "cert_ssh_auth_type": "key", "cert_ssh_key": "", "cert_ssh_password": "", "cert_ssh_remote_dir": "", "cert_ssh_host_key": "",
		"backup_enabled": true, "backup_interval_hours": 24, "backup_time": "03:00", "backup_dir": filepath.Join(a.cfg.DataDir, "backups"), "backup_retention_count": 15, "backup_last_run": "",
	}
}
func (a *App) secretSetting(key string) string {
	raw := strings.TrimSpace(str(a.setting(key)))
	if raw == "" {
		return ""
	}
	if plain, err := a.decrypt(raw); err == nil {
		return plain
	}
	// Legacy plaintext token stored before encryption was added: migrate in place.
	a.set(key, a.encrypt(raw))
	return raw
}
func (a *App) setSecretSetting(key, plain string) {
	trimmed := strings.TrimSpace(plain)
	if trimmed == "" {
		a.set(key, "")
		return
	}
	a.set(key, a.encrypt(trimmed))
}
func (a *App) setting(key string) any {
	row := one(a.db, `SELECT SettingValue FROM Setting WHERE SettingKey=?`, key)
	if row == nil {
		return a.defaults()[key]
	}
	var v any
	check(json.Unmarshal([]byte(str(row["SettingValue"])), &v))
	return v
}
func (a *App) set(key string, value any) {
	if _, ok := a.defaults()[key]; !ok {
		fail(422, "Unknown setting: "+key)
	}
	b, err := json.Marshal(value)
	check(err)
	execute(a.db, `INSERT INTO Setting(SettingKey,SettingValue,UpdatedDate) VALUES(?,?,?) ON CONFLICT(SettingKey) DO UPDATE SET SettingValue=excluded.SettingValue,UpdatedDate=excluded.UpdatedDate`, key, string(b), now())
}
func (a *App) settings() M {
	out := a.defaults()
	hasCFToken := false
	hasSSHKey := false
	hasSSHPassword := false
	for _, row := range query(a.db, `SELECT SettingKey,SettingValue FROM Setting`) {
		key := str(row["SettingKey"])
		if _, ok := out[key]; ok {
			var v any
			check(json.Unmarshal([]byte(str(row["SettingValue"])), &v))
			switch key {
			case "cf_dns_token":
				hasCFToken = strings.TrimSpace(str(v)) != ""
				out[key] = ""
				continue
			case "cert_ssh_key":
				hasSSHKey = strings.TrimSpace(str(v)) != ""
				out[key] = ""
				continue
			case "cert_ssh_password":
				hasSSHPassword = strings.TrimSpace(str(v)) != ""
				out[key] = ""
				continue
			case "se_password":
				out[key] = ""
				continue
			}
			out[key] = v
		}
	}
	out["has_cf_dns_token"] = hasCFToken
	out["has_cert_ssh_key"] = hasSSHKey
	out["has_cert_ssh_password"] = hasSSHPassword
	return out
}
func (a *App) audit(user M, action, target, key, detail string, serverIDs ...int64) {
	var id any
	name := "system"
	if user != nil {
		id = user["UserID"]
		name = str(user["Username"])
	}
	var serverID any
	if user != nil && target != "panel" && target != "panel_user" {
		serverID = user["ServerID"]
	}
	if len(serverIDs) > 0 {
		serverID = serverIDs[0]
	}
	execute(a.db, `INSERT INTO AuditLog(ServerID,UserID,Username,Action,TargetType,TargetKey,Detail,CreatedDate) VALUES(?,?,?,?,?,?,?,?)`, serverID, id, name, action, target, key, detail, now())
}
