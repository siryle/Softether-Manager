package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestCertificatePinAlwaysEnforced(t *testing.T) {
	a := testApp(t)
	token := admin(t, a)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req M
		check(json.NewDecoder(r.Body).Decode(&req))
		writeJSON(w, 200, M{"jsonrpc": "2.0", "id": req["id"], "result": M{"ServerVersionString_str": "test"}})
	})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	u, _ := url.Parse(server.URL)
	host, portText, _ := strings.Cut(u.Host, ":")
	port, err := strconv.Atoi(portText)
	check(err)
	a.saveConnection(host, port, ptr("secret"), "header")
	id := a.serverIDFromContext(context.Background())
	if _, err := a.client().Call(context.Background(), "GetServerInfo", nil); err != nil {
		t.Fatal(err)
	}
	// A stale pin must block the credential even if a legacy payload includes skip_cert_check.
	execute(a.db, `UPDATE RPCTrust SET Fingerprint=? WHERE Host=? AND Port=?`, strings.Repeat("0", 64), host, port)
	a.connectionMu.Lock()
	a.invalidateClientLocked(id)
	a.connectionMu.Unlock()
	if _, err := a.client().Call(context.Background(), "GetServerInfo", nil); err == nil || !strings.Contains(err.Error(), "certificate changed") {
		t.Fatalf("stale pin was not enforced: %v", err)
	}

	path := "/api/v1/servers/" + strconv.FormatInt(id, 10)
	status, out, _ := call(t, a, "PUT", path, M{"skip_cert_check": true, "skip_test": true}, token)
	if status != 200 {
		t.Fatalf("PUT %s: %d %v", path, status, out)
	}
	if _, exists := out["skip_cert_check"]; exists {
		t.Fatalf("expected skip_cert_check removed from server response, got %v", out)
	}
	if _, err := a.client().Call(context.Background(), "GetServerInfo", nil); err == nil || !strings.Contains(err.Error(), "certificate changed") {
		t.Fatalf("certificate pin must remain enforced: %v", err)
	}
}
