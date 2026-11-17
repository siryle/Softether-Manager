package manager

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// The base64 SHA-0 hash SoftEther keeps for a password: 20 bytes.
var testPasswordHash = func() string {
	raw := make([]byte, 20)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(raw)
}()

// fakeHub answers the user RPCs the bulk endpoints use and records the calls.
func fakeHub(t *testing.T) (*httptest.Server, *[]M) {
	t.Helper()
	var mu sync.Mutex
	calls := []M{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     string `json:"id"`
			Method string `json:"method"`
			Params M      `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		calls = append(calls, M{"method": req.Method, "params": req.Params})
		mu.Unlock()
		result := M{}
		switch req.Method {
		case "EnumUser":
			result = M{"UserList": []any{M{
				"Name_str": "alice", "Realname_utf": "Alice", "Note_utf": "ops, on call",
				"GroupName_str": "staff", "AuthType_u32": 1, "IsExpiresFilled_bool": true,
				"Expires_dt": "2027-01-02T03:04:05.000Z",
			}}}
		case "GetUser":
			result = M{"Name_str": req.Params["Name_str"], "AuthType_u32": 1, "IsExpiresFilled_bool": false, "HashedKey_bin": testPasswordHash}
		}
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req.ID, "result": result})
	})
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server, &calls
}

func bulkApp(t *testing.T) (*App, *[]M) {
	t.Helper()
	a := testApp(t)
	server, calls := fakeHub(t)
	u, _ := url.Parse(server.URL)
	host, portText, _ := strings.Cut(u.Host, ":")
	a.saveConnection(host, int(integer(portText)), ptr("secret"), "header")
	return a, calls
}

func TestUserExportWritesCSV(t *testing.T) {
	a, _ := bulkApp(t)
	token := admin(t, a)
	status, out, _ := call(t, a, "GET", "/api/v1/hubs/VPN/users/export", nil, token)
	if status != 200 {
		t.Fatalf("export: %d %v", status, out)
	}
	lines := strings.Split(strings.TrimSpace(str(out["csv"])), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + one row, got %q", str(out["csv"]))
	}
	if lines[0] != "name,realname,note,group,auth_type,password,password_hash,expires" {
		t.Fatalf("header: %q", lines[0])
	}
	// The hash travels so a re-import keeps the original password working.
	if want := `alice,Alice,"ops, on call",staff,1,,` + testPasswordHash + `,2027-01-02T03:04:05.000Z`; lines[1] != want {
		t.Fatalf("row: %q want %q", lines[1], want)
	}
	if str(out["filename"]) != "VPN-users.csv" || integer(out["count"]) != 1 {
		t.Fatalf("metadata: %v", out)
	}
}

func TestUserImportCreatesSkipsAndReports(t *testing.T) {
	a, calls := bulkApp(t)
	token := admin(t, a)
	body := M{"csv": strings.Join([]string{
		"name,realname,note,group,auth_type,password,expires",
		"bob,Bob Chen,new hire,,password,s3cret,",
		"alice,Alice,already here,,1,whatever,",
		"carol,Carol,bad type,,martian,x,",
		"dave,Dave,no password,,1,,",
	}, "\n")}
	status, out, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", body, token)
	if status != 200 {
		t.Fatalf("import: %d %v", status, out)
	}
	if created, _ := out["created"].([]any); len(created) != 1 || str(created[0]) != "bob" {
		t.Fatalf("created: %v", out["created"])
	}
	if skipped, _ := out["skipped"].([]any); len(skipped) != 1 || str(skipped[0].(map[string]any)["name"]) != "alice" {
		t.Fatalf("skipped: %v", out["skipped"])
	}
	failed, _ := out["failed"].([]any)
	if len(failed) != 2 {
		t.Fatalf("failed: %v", out["failed"])
	}
	cases := map[string]string{}
	for _, item := range failed {
		row := item.(map[string]any)
		cases[str(row["name"])] = str(row["error"])
	}
	if !strings.Contains(cases["carol"], "auth_type") || !strings.Contains(cases["dave"], "password") {
		t.Fatalf("failures: %v", cases)
	}

	// The created user reaches SoftEther with the CSV's values...
	found := false
	for _, c := range *calls {
		if str(c["method"]) != "CreateUser" {
			continue
		}
		params := c["params"].(map[string]any)
		if str(params["Name_str"]) != "bob" || str(params["Auth_Password_str"]) != "s3cret" || integer(params["AuthType_u32"]) != 1 {
			t.Fatalf("CreateUser payload: %v", params)
		}
		found = true
	}
	if !found {
		t.Fatal("no CreateUser call")
	}
	// ...and the exported .vpn credential is vaulted for embedding.
	if one(a.db, `SELECT 1 FROM VpnUserCredential WHERE HubName='VPN' AND UserName='bob'`) == nil {
		t.Fatal("credential was not stored")
	}
}

func TestUserImportAssignsAGroupWithSetUser(t *testing.T) {
	a, calls := bulkApp(t)
	token := admin(t, a)
	body := M{"csv": "name,password,group\nerin,pw,staff\n"}
	status, out, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", body, token)
	if status != 200 || len(out["created"].([]any)) != 1 {
		t.Fatalf("import: %d %v", status, out)
	}
	merged := false
	for _, c := range *calls {
		if str(c["method"]) == "SetUser" && str(c["params"].(map[string]any)["GroupName_str"]) == "staff" {
			merged = true
		}
	}
	if !merged {
		t.Fatal("group was not applied with SetUser")
	}
}

func TestUserImportRejectsAFileWithoutANameColumn(t *testing.T) {
	a, _ := bulkApp(t)
	token := admin(t, a)
	status, _, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", M{"csv": "user,password\nbob,pw\n"}, token)
	if status != 422 {
		t.Fatalf("accepted a header without a name column: %d", status)
	}
	if one(a.db, `SELECT 1 FROM AuditLog WHERE Action='user.imported'`) != nil {
		t.Fatal("audited an import that never started")
	}
}

func TestUserImportAcceptsAPasswordHash(t *testing.T) {
	a, calls := bulkApp(t)
	token := admin(t, a)
	body := M{"csv": "name,auth_type,password_hash\nfrank,password," + testPasswordHash + "\n"}
	status, out, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", body, token)
	if status != 200 || len(out["created"].([]any)) != 1 {
		t.Fatalf("import: %d %v", status, out)
	}
	payload := M{}
	for _, c := range *calls {
		if str(c["method"]) == "CreateUser" {
			payload = c["params"].(map[string]any)
		}
	}
	if str(payload["HashedKey_bin"]) != testPasswordHash {
		t.Fatalf("hash not sent: %v", payload)
	}
	if _, ok := payload["Auth_Password_str"]; ok {
		t.Fatalf("plaintext password sent alongside the hash: %v", payload)
	}
	// The vault keeps the same hash, so .vpn exports still embed a credential.
	if got := a.storedCredential(a.serverIDFromContext(context.Background()), "VPN", "frank"); got != testPasswordHash {
		t.Fatalf("vaulted credential: %q", got)
	}
}

func TestUserImportRejectsAMalformedHash(t *testing.T) {
	a, _ := bulkApp(t)
	token := admin(t, a)
	body := M{"csv": "name,auth_type,password_hash\ngina,password,bm90LWEtaGFzaA==\n"}
	status, out, _ := call(t, a, "POST", "/api/v1/hubs/VPN/users/import", body, token)
	if status != 200 {
		t.Fatalf("import: %d", status)
	}
	failed, _ := out["failed"].([]any)
	if len(failed) != 1 || !strings.Contains(str(failed[0].(map[string]any)["error"]), "password_hash") {
		t.Fatalf("failed: %v", out["failed"])
	}
}
