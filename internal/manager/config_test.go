package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestCLIPasswordTrimsNewline(t *testing.T) {
	if got := cliPassword("secret\n", false); got != "secret\n" {
		t.Fatalf("direct password should be unchanged: %q", got)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	os.Stdin = r
	go func() {
		defer w.Close()
		_, _ = w.WriteString("my_password\r\n")
	}()
	got := cliPassword("", true)
	if got != "my_password" {
		t.Fatalf("expected 'my_password', got %q", got)
	}
}

func TestLoginLimiterEvictionAtCapacity(t *testing.T) {
	var limiter loginLimiter
	at := time.Now()
	for i := range 4096 {
		if !limiter.take(string(rune(i)), at) {
			t.Fatalf("first 4096 should succeed at index %d", i)
		}
	}
	// 4097th IP should succeed via eviction, not return false
	if !limiter.take("overflow_ip", at) {
		t.Fatal("limiter rejected new IP when at capacity instead of evicting oldest")
	}
}
