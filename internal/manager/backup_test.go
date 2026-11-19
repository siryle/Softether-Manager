package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShouldRunBackup(t *testing.T) {
	now := time.Date(2026, 11, 17, 3, 30, 0, 0, time.UTC)

	// Disabled
	if shouldRunBackup(false, 24, "03:00", "", now) {
		t.Fatalf("expected false when disabled")
	}

	// 24h interval, no last run -> should run
	if !shouldRunBackup(true, 24, "03:00", "", now) {
		t.Fatalf("expected true on first run")
	}

	// Last run today at 03:05 -> should not run at 03:30
	lastRunToday := time.Date(2026, 11, 17, 3, 5, 0, 0, time.UTC).Format(time.RFC3339)
	if shouldRunBackup(true, 24, "03:00", lastRunToday, now) {
		t.Fatalf("expected false when already run today")
	}

	// Last run yesterday at 03:05 -> should run today at 03:30
	lastRunYesterday := time.Date(2026, 11, 16, 3, 5, 0, 0, time.UTC).Format(time.RFC3339)
	if !shouldRunBackup(true, 24, "03:00", lastRunYesterday, now) {
		t.Fatalf("expected true when last run yesterday")
	}

	// Interval 6h, last run 2h ago -> false
	lastRun2h := now.Add(-2 * time.Hour).Format(time.RFC3339)
	if shouldRunBackup(true, 6, "", lastRun2h, now) {
		t.Fatalf("expected false when interval has not elapsed")
	}

	// Interval 6h, last run 7h ago -> true
	lastRun7h := now.Add(-7 * time.Hour).Format(time.RFC3339)
	if !shouldRunBackup(true, 6, "", lastRun7h, now) {
		t.Fatalf("expected true when interval elapsed")
	}
}

func TestRotateAutoBackups(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 20 auto backups and 2 manual backups
	for i := 1; i <= 20; i++ {
		fname := fmt.Sprintf("vpn_server_auto_202611%02d_030000.config", i)
		if err := os.WriteFile(filepath.Join(tmpDir, fname), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 2; i++ {
		fname := fmt.Sprintf("vpn_server_manual_202611%02d_120000.config", i)
		if err := os.WriteFile(filepath.Join(tmpDir, fname), []byte("manual"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Rotate with max 15
	if err := rotateAutoBackups(tmpDir, 15); err != nil {
		t.Fatalf("rotateAutoBackups failed: %v", err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	autoCount := 0
	manualCount := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) == ".config" {
			if len(e.Name()) > 16 && e.Name()[:16] == "vpn_server_auto_" {
				autoCount++
			} else {
				manualCount++
			}
		}
	}

	if autoCount != 15 {
		t.Fatalf("expected exactly 15 auto backups remaining, got %d", autoCount)
	}
	if manualCount != 2 {
		t.Fatalf("expected manual backups preserved (2), got %d", manualCount)
	}

	// Verify the oldest auto backups (1 to 5) were removed, and latest (6 to 20) remain
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("vpn_server_auto_202611%02d_030000.config", i)
		if _, err := os.Stat(filepath.Join(tmpDir, fname)); !os.IsNotExist(err) {
			t.Fatalf("expected old backup %s to be deleted", fname)
		}
	}
	for i := 6; i <= 20; i++ {
		fname := fmt.Sprintf("vpn_server_auto_202611%02d_030000.config", i)
		if _, err := os.Stat(filepath.Join(tmpDir, fname)); os.IsNotExist(err) {
			t.Fatalf("expected recent backup %s to be kept", fname)
		}
	}
}

func TestIsSafeBackupFilename(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"vpn_server_auto_20261117_030000.config", true},
		{"vpn_server_manual_20261117_120000.config", true},
		{"vpn_server_s2_auto_20261117_030000.config", true},
		{"../vpn_server_auto_20261117_030000.config", false},
		{"/etc/passwd", false},
		{"vpn_server_auto.sh", false},
		{"", false},
		{"vpn_server_something.config", true},
	}
	for _, tc := range cases {
		got := isSafeBackupFilename(tc.name)
		if got != tc.valid {
			t.Errorf("isSafeBackupFilename(%q) = %v, expected %v", tc.name, got, tc.valid)
		}
	}
}

func TestEnrichSessions(t *testing.T) {
	a := testApp(t)

	rawSessions := []M{
		{
			"Name_str":           "SID-user1-1",
			"Username_str":       "user1",
			"ClientIP_ip":        "123.182.152.119",
			"Hostname_str":       "123.182.152.119", // Hostname equals public IP!
			"RemoteHostname_str": "S20M",           // SoftEther server name!
		},
		{
			"Name_str":           "SID-user2-2",
			"Username_str":       "user2",
			"ClientIP_ip":        "203.0.113.5",
			"Hostname_str":       "client.example.com", // Real PTR hostname
			"RemoteHostname_str": "S20M",
		},
	}

	enriched := a.enrichSessions(context.Background(), "DEFAULT", rawSessions)

	// user1's hostname should be cleared because it's just an IP address identical to clientIP
	if enriched[0]["Hostname_str"] != "" {
		t.Fatalf("expected user1 hostname cleared from IP, got %v", enriched[0]["Hostname_str"])
	}

	// user2's real hostname should be preserved
	if enriched[1]["Hostname_str"] != "client.example.com" {
		t.Fatalf("expected user2 hostname preserved, got %v", enriched[1]["Hostname_str"])
	}

	// Test enrichSessionStatus: ServerName_str must not become the client host!
	status := a.enrichSessionStatus(context.Background(), "DEFAULT", "SID-user1-1", M{
		"Client_Ip_Address_ip":             "123.182.152.119",
		"ServerName_str":                   "S20M",
		"SessionStatus_ClientHostName_str": "123.182.152.119",
		"ClientHostname_str":               "I4P228986", // Reported client host
		"ServerPort_u32":                   0,
		"ServerPort2_u32":                  2768633856, // Endian32(1701)
		"ClientPort_u32":                   2768633856, // Endian32(1701)
	})

	if status["ClientHostname_str"] != "I4P228986" {
		t.Fatalf("expected client reported hostname 'I4P228986', got %v", status["ClientHostname_str"])
	}
	if integer(status["ServerPort_u32"]) != 1701 || integer(status["ClientPort_u32"]) != 1701 {
		t.Fatalf("expected decoded ports 1701/1701, got %v/%v", status["ServerPort_u32"], status["ClientPort_u32"])
	}
	if sPort, cPort := extractSessionPorts(M{"ServerPort_u32": 443, "ClientPort_u32": 54321}); sPort != 443 || cPort != 54321 {
		t.Fatalf("expected direct ports 443/54321, got %d/%d", sPort, cPort)
	}

	// Ensure IPv4 check helper
	if !isIPv4("192.168.6.189") {
		t.Fatalf("expected 192.168.6.189 to be IPv4")
	}
	if isIPv4("fe80::90bd:bfd3:605a:6c95") {
		t.Fatalf("expected fe80::... not to be IPv4")
	}

	// Test enrichSessions populates ActiveNatTcp_u32 from EnumNAT for matching session virtual IPs.
	fake := attachSoftEther(t, a)
	fake.natTable = []M{
		{"SrcIp_ip": "192.168.6.189", "Protocol_u32": 0},
		{"SrcIp_ip": "192.168.6.189", "Protocol_u32": 0},
		{"SrcIp_ip": "192.168.6.189", "Protocol_u32": 1}, // UDP NAT entry, excluded
		{"SrcIp_ip": "192.168.6.200", "Protocol_u32": 0}, // Different client IP, excluded
	}
	l2tpEnriched := a.enrichSessions(context.Background(), "DEFAULT", []M{
		{"Name_str": "SID/ABC", "Username_str": "ALICE", "ClientIP_ip": "192.0.2.1"},
	})
	if integer(l2tpEnriched[0]["ActiveNatTcp_u32"]) != 2 {
		t.Fatalf("expected ActiveNatTcp_u32 = 2, got %v", l2tpEnriched[0]["ActiveNatTcp_u32"])
	}
}

func TestBackupEndpoints(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)

	// 1. Get initial backup settings
	status, s, _ := call(t, a, "GET", "/api/v1/server/backup-settings", nil, token)
	if status != 200 {
		t.Fatalf("get backup-settings failed: %d", status)
	}
	if integer(s["retention_count"]) != 15 {
		t.Fatalf("expected retention_count 15, got %v", s["retention_count"])
	}

	// 2. Update backup settings
	status, updated, _ := call(t, a, "PUT", "/api/v1/server/backup-settings", M{
		"enabled":        true,
		"interval_hours": 12,
		"backup_time":    "04:00",
	}, token)
	if status != 200 {
		t.Fatalf("update backup-settings failed: %d", status)
	}
	if integer(updated["interval_hours"]) != 12 {
		t.Fatalf("expected interval_hours 12, got %v", updated["interval_hours"])
	}
	if str(updated["backup_time"]) != "04:00" {
		t.Fatalf("expected backup_time 04:00, got %v", updated["backup_time"])
	}

	// 3. List backups
	status, listRes, _ := call(t, a, "GET", "/api/v1/server/backups", nil, token)
	if status != 200 {
		t.Fatalf("list backups failed: %d", status)
	}
	if listRes["backups"] == nil {
		t.Fatalf("expected backups list, got nil")
	}

	// 4. Manually write a mock backup file into backup_dir to test download & delete
	dir := str(s["backup_dir"])
	testFile := "vpn_server_manual_test_20261117_120000.config"
	_ = os.WriteFile(filepath.Join(dir, testFile), []byte("# Mock SoftEther Config\nDeclare root\n{\n}"), 0644)

	// Download test
	status, _, rec := call(t, a, "GET", "/api/v1/server/backups/"+testFile+"/download", nil, token)
	if status != 200 {
		t.Fatalf("download backup failed, code %d: %s", status, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Mock SoftEther Config") {
		t.Fatalf("expected config content in downloaded file, got: %s", rec.Body.String())
	}

	// Delete test
	status, _, _ = call(t, a, "DELETE", "/api/v1/server/backups/"+testFile, nil, token)
	if status != 200 {
		t.Fatalf("delete backup failed, code %d", status)
	}
	if _, err := os.Stat(filepath.Join(dir, testFile)); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted")
	}
}
