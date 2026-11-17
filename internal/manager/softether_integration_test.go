//go:build darwin || linux

package manager

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Run with SEM_TEST_VPNSERVER_DIR pointing to an official vpnserver + hamcore.se2 directory.
func TestSoftEtherLiveQuotaReset(t *testing.T) {
	binaryDir := os.Getenv("SEM_TEST_VPNSERVER_DIR")
	if binaryDir == "" {
		t.Skip("set SEM_TEST_VPNSERVER_DIR to run against real SoftEther VPN sessions")
	}
	a := testApp(t)
	token := admin(t, a)
	target, port, password := startLiveSoftEther(t, a, binaryDir)
	source, _, _ := startLiveSoftEther(t, a, binaryDir)
	a.saveConnection("127.0.0.1", port, &password, "header")
	ctx := context.Background()
	rpc := func(client *RPCClient, method string, params M) M {
		t.Helper()
		out, err := client.Call(ctx, method, params)
		if err != nil {
			t.Fatalf("real SoftEther %s: %v", method, err)
		}
		return out
	}
	for _, client := range []*RPCClient{target, source} {
		rpc(client, "CreateHub", M{"HubName_str": "DEFAULT", "Online_bool": true, "HubType_u32": 0})
	}
	vpnPassword, err := secret(filepath.Join(t.TempDir(), "vpn-password"), "")
	check(err)
	rpc(target, "CreateUser", M{"HubName_str": "DEFAULT", "Name_str": "admin", "AuthType_u32": 1, "Auth_Password_str": vpnPassword, "UsePolicy_bool": false})
	user := rpc(target, "GetUser", M{"HubName_str": "DEFAULT", "Name_str": "admin"})
	if _, exists := user["UsePolicy_bool"]; exists {
		t.Fatal("the fixture must exercise SoftEther's omitted inherited-policy fields")
	}
	rpc(source, "CreateLink", M{"HubName_Ex_str": "DEFAULT", "AccountName_utf": "reset-test", "Hostname_str": "127.0.0.1", "Port_u32": port, "HubName_str": "DEFAULT", "AuthType_u32": 1, "Username_str": "admin", "HashedPassword_bin": user["HashedKey_bin"], "UseEncrypt_bool": true, "MaxConnection_u32": 1, "AdditionalConnectionInterval_u32": 1})
	rpc(source, "SetLinkOnline", M{"HubName_str": "DEFAULT", "AccountName_utf": "reset-test"})
	liveSession := func() string {
		for _, session := range list(rpc(target, "EnumSession", M{"HubName_str": "DEFAULT"})["SessionList"]) {
			if fold(str(session["Username_str"])) == "admin" {
				return str(session["Name_str"])
			}
		}
		return ""
	}
	waitForSession := func() string {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for time.Now().Before(deadline) {
			if key := liveSession(); key != "" {
				return key
			}
			time.Sleep(100 * time.Millisecond)
		}
		status := rpc(source, "GetLinkStatus", M{"HubName_Ex_str": "DEFAULT", "AccountName_utf": "reset-test"})
		for _, field := range []string{"Connected_bool", "Active_bool", "LastError_u32", "RetryCount_u32"} {
			t.Logf("cascade %s=%v", field, status[field])
		}
		for _, session := range list(rpc(target, "EnumSession", M{"HubName_str": "DEFAULT"})["SessionList"]) {
			t.Logf("target session name=%s user=%s key present=%t", session["Name_str"], session["Username_str"], str(session["UniqueId_bin"]) != "")
		}
		t.Fatal("the real VPN cascade did not connect")
		return ""
	}
	key := waitForSession()
	a.sampleTraffic(ctx)
	for _, table := range []string{"HubTrafficSample", "UserTrafficSample"} {
		if integer(one(a.db, `SELECT COUNT(*) n FROM `+table+` WHERE ServerID=1`)["n"]) != 1 {
			t.Fatal("real SoftEther traffic counters were not recorded", table)
		}
	}
	path := "/api/v1/quotas/user/DEFAULT/admin"
	for _, scenario := range []string{"new baseline", "existing baseline", "already restored policy"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "already restored policy" {
				a.saveQuota(1, "user", "DEFAULT", "admin", 0, "total", false)
				execute(a.db, `UPDATE TrafficQuota SET RestoreState='{"UsePolicy_bool":false}',EnforcedDate=? WHERE ServerID=1`, now())
			}
			before := rpc(target, "GetUser", M{"HubName_str": "DEFAULT", "Name_str": "admin"})
			// UpdatedTime is millisecond resolution; a rewrite must be observable.
			time.Sleep(10 * time.Millisecond)
			status, out, _ := call(t, a, "POST", path+"/reset", nil, token)
			if status != 200 || integer(out["used_bytes"]) != 0 || boolean(out["blocked"]) {
				t.Fatal("real VPN reset failed", status, out)
			}
			after := rpc(target, "GetUser", M{"HubName_str": "DEFAULT", "Name_str": "admin"})
			if before["UpdatedTime_dt"] != after["UpdatedTime_dt"] {
				t.Fatal("clearing the panel counters rewrote the real VPN user")
			}
			if liveSession() != key {
				t.Fatal("clearing transfer replaced or disconnected the live VPN session")
			}
		})
	}
	// Exercise genuine blocking and verify cancellation permits a new authenticated session.
	a.saveQuota(1, "user", "DEFAULT", "admin", 1, "total", true)
	row := a.quotaRow(1, "user", "DEFAULT", "admin")
	a.blockQuota(ctx, row)
	a.cutQuotaSessions(ctx, row)
	status, out, _ := call(t, a, "DELETE", path, nil, token)
	if status != 200 {
		t.Fatal("real VPN policy did not restore after cancellation", status, out)
	}
	restored := rpc(target, "GetUser", M{"HubName_str": "DEFAULT", "Name_str": "admin"})
	if restored["HashedKey_bin"] != user["HashedKey_bin"] {
		t.Fatal("quota enforcement or restoration changed the real VPN password hash")
	}
	rpc(source, "SetLinkOffline", M{"HubName_str": "DEFAULT", "AccountName_utf": "reset-test"})
	rpc(source, "SetLinkOnline", M{"HubName_str": "DEFAULT", "AccountName_utf": "reset-test"})
	if waitForSession() == key {
		t.Fatal("the blocking control did not disconnect its original session")
	}
	user = rpc(target, "GetUser", M{"HubName_str": "DEFAULT", "Name_str": "admin"})
	if boolean(user["UsePolicy_bool"]) {
		t.Fatal("cancellation left an explicit deny policy on the real VPN user")
	}
	t.Log("real VPN session survived all resets and reconnected after quota cancellation")
}

func startLiveSoftEther(t *testing.T, a *App, binaryDir string) (*RPCClient, int, string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"vpnserver", "hamcore.se2"} {
		data, err := os.ReadFile(filepath.Join(binaryDir, name))
		if err != nil {
			t.Fatal(err)
		}
		check(os.WriteFile(filepath.Join(dir, name), data, 0700))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	check(err)
	port := listener.Addr().(*net.TCPAddr).Port
	check(listener.Close())
	password, err := secret(filepath.Join(dir, "admin-password"), "")
	check(err)
	config := fmt.Sprintf(`declare root
{
 declare ServerConfiguration
 {
  byte HashedPassword %s
  bool DisableNatTraversal true
  bool DisableJsonRpcWebApi false
  bool UseKeepConnect false
 }
 declare ListenerList
 {
  declare Listener0
  {
   bool Enabled true
   bool DisableDos true
   uint Port %d
  }
 }
 declare VirtualHUB
 {
 }
 declare DDnsClient
 {
  bool Disabled true
 }
}
`, base64.StdEncoding.EncodeToString(sha0([]byte(password))), port)
	check(os.WriteFile(filepath.Join(dir, "vpn_server.config"), []byte(config), 0600))
	cmd := exec.Command(filepath.Join(dir, "vpnserver"), "execsvc")
	if runtime.GOOS == "darwin" {
		profile := fmt.Sprintf(`(version 1)(allow default)(deny network*)(allow network-bind (local tcp "*:%d"))(allow network-inbound (local tcp "localhost:%d"))(allow network-outbound (remote tcp "localhost:*"))`, port, port)
		cmd = exec.Command("/usr/bin/sandbox-exec", "-p", profile, filepath.Join(dir, "vpnserver"), "execsvc")
	}
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	check(cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	})
	client := a.newRPCClient("127.0.0.1", port, password, "header", false)
	t.Cleanup(client.Close)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("real SoftEther exited during startup: %s", output.String())
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = client.Call(ctx, "GetServerInfo", nil)
		cancel()
		if err == nil {
			return client, port, password
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Keep failures limited to setup, without exposing administrator credentials.
	t.Fatal("real SoftEther did not become ready within 15 seconds")
	return nil, 0, ""
}
