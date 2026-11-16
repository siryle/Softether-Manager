package manager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeVersionSources(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SEM_APP_ROOT", root)
	t.Setenv("SEM_VERSION", "")
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("1.0.17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := LoadConfig().Version; got != "1.0.17" {
		t.Fatalf("file version: %q", got)
	}
	saved := buildVersion
	t.Cleanup(func() { buildVersion = saved })
	buildVersion = "v2.3.4"
	if got := LoadConfig().Version; got != "v2.3.4" {
		t.Fatalf("release binary must use stamped tag: %q", got)
	}
	t.Setenv("SEM_VERSION", "v2.3.5")
	if got := LoadConfig().Version; got != "v2.3.5" {
		t.Fatalf("explicit version override: %q", got)
	}
}
