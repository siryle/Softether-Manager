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
	}
	dsn := path
	if path != ":memory:" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		u := url.URL{Scheme: "file", Path: abs}
		dsn = u.String()
	}
	dsn += "?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)"
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
	// Carry the first connection forward from the former multi-server layout.
	columns, err := tableColumns(tx, "Server")
	if err != nil {
		return err
	}
	if len(columns) > 0 {
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
	// Python also rebuilt disposable series carrying the old ServerID dimension.
	for _, table := range []string{"UserTrafficSample", "HubTrafficSample"} {
		cols, e := tableColumns(tx, table)
		if e != nil {
			return e
		}
		if cols["ServerID"] {
			if _, e = tx.Exec(`DROP TABLE "` + table + `"`); e != nil {
				return e
			}
		}
	}
	// Existing installations retain their accounts, settings, samples and quotas.
	for _, migration := range []struct{ table, column, ddl string }{
		{"VpnSessionSample", "DownloadBytes", `INTEGER NOT NULL DEFAULT 0`}, {"VpnSessionSample", "UploadBytes", `INTEGER NOT NULL DEFAULT 0`},
		{"VpnSessionTrafficSample", "TotalBytes", `INTEGER NOT NULL DEFAULT 0`}, {"TrafficQuota", "BaseSendBytes", `INTEGER NOT NULL DEFAULT 0`}, {"TrafficQuota", "BaseRecvBytes", `INTEGER NOT NULL DEFAULT 0`},
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
	if _, err = tx.Exec(schema); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE TrafficQuota SET LastSendBytes=MAX(0,LastSendBytes),LastRecvBytes=MAX(0,LastRecvBytes) WHERE LastSendBytes<0 OR LastRecvBytes<0`); err != nil {
		return err
	}
	// Keep obsolete deployment records for operators who still need their history.
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
		"domain": "", "https_port": 443, "acme_email": "", "acme_staging": false, "domain_only": false,
	}
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
	for _, row := range query(a.db, `SELECT SettingKey,SettingValue FROM Setting`) {
		key := str(row["SettingKey"])
		if _, ok := out[key]; ok {
			var v any
			check(json.Unmarshal([]byte(str(row["SettingValue"])), &v))
			out[key] = v
		}
	}
	return out
}
func (a *App) audit(user M, action, target, key, detail string) {
	var id any
	name := "system"
	if user != nil {
		id = user["UserID"]
		name = str(user["Username"])
	}
	execute(a.db, `INSERT INTO AuditLog(UserID,Username,Action,TargetType,TargetKey,Detail,CreatedDate) VALUES(?,?,?,?,?,?,?)`, id, name, action, target, key, detail, now())
}
