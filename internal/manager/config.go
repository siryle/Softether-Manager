package manager

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// buildVersion is stamped into release executables; source builds read VERSION.
var buildVersion string

type Config struct {
	Root, DataDir, Database, BindHost, WebPath, SecretKey, EncryptionKey, Version, Repo, ServiceName string
	BindPort, SessionMinutes                                                                         int
	CORS                                                                                             []string
	SEHost, SEPassword, SEAuth, AdminUsername, AdminPassword                                         string
	SEPort                                                                                           int
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return fallback
}
func envInt(key string, fallback int) int {
	n, err := strconv.Atoi(env(key, ""))
	if err != nil {
		return fallback
	}
	return n
}
func installedRepository() string {
	b, _ := os.ReadFile(env("SEM_CLI_ENV_PATH", "/usr/local/share/softether-manager/cli.env"))
	for _, line := range strings.Split(string(b), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(key) == "RELEASE_REPO" {
			if repo := strings.Trim(strings.TrimSpace(value), `"'`); repo != "" {
				return repo
			}
		}
	}
	return env("SEM_RELEASE_REPO", "siryle/Softether-Manager")
}
func LoadConfig() Config {
	root := env("SEM_APP_ROOT", "")
	if root == "" {
		root, _ = os.Getwd()
		if _, err := os.Stat(filepath.Join(root, "VERSION")); err != nil {
			if exe, e := os.Executable(); e == nil {
				root = filepath.Dir(exe)
			}
		}
	}
	version := env("SEM_VERSION", "")
	if version == "" {
		version = buildVersion
	}
	if version == "" {
		b, _ := os.ReadFile(filepath.Join(root, "VERSION"))
		version = strings.TrimSpace(string(b))
		if version == "" {
			version = "0.0.0-dev"
		}
	}
	data := env("SEM_DATA_DIR", filepath.Join(root, "data"))
	database := env("SEM_DATABASE_URL", "")
	if strings.HasPrefix(database, "sqlite:///") {
		database = strings.TrimPrefix(database, "sqlite:///")
	}
	if database == "" {
		database = filepath.Join(data, "manager.db")
	}
	return Config{Root: root, DataDir: data, Database: database, BindHost: env("SEM_BIND_HOST", "0.0.0.0"), BindPort: envInt("SEM_BIND_PORT", 8000), WebPath: strings.Trim(env("SEM_WEB_PATH", ""), "/"), SecretKey: env("SEM_SECRET_KEY", ""), EncryptionKey: env("SEM_ENCRYPTION_PASSPHRASE", ""), SessionMinutes: envInt("SEM_SESSION_EXPIRE_MINUTES", 10080), Version: version, Repo: installedRepository(), ServiceName: env("SEM_SERVICE_NAME", "softether-manager"), CORS: strings.Split(env("SEM_CORS_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000"), ","), SEHost: env("SEM_SE_HOST", "127.0.0.1"), SEPort: envInt("SEM_SE_PORT", 5555), SEPassword: env("SEM_SE_PASSWORD", ""), SEAuth: env("SEM_SE_AUTH", "header"), AdminUsername: env("SEM_ADMIN_USERNAME", ""), AdminPassword: env("SEM_ADMIN_PASSWORD", "")}
}
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05+00:00") }
