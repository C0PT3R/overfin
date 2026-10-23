package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The migration module copies an old account's history into a new one,
// through the visitor route the core guards.
func TestMigrationClaim(t *testing.T) {
	srv, _ := newServer(t)
	claim := func(who, body string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/me/claim", strings.NewReader(body))
		req.Header.Set("Remote-User", who)
		req.Header.Set("Content-Type", "application/json")
		res, err := client(srv, who).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// A visitor is told they can bring their account, and how.
	if code, body := getAs(t, srv, "newcomer", "/api/v1/me"); code != 404 || !strings.Contains(body, `"claim":"copy"`) {
		t.Errorf("setup offer: %d %s", code, body)
	}
	if code, _ := claim("newcomer", `{"username":"old","password":"pw","extra":1}`); code != 400 {
		t.Errorf("unknown field: %d", code)
	}
	if code, _ := claim("newcomer", `{"username":"","password":"pw"}`); code != 400 {
		t.Errorf("no username: %d", code)
	}
	if code, body := claim("newcomer", `{"username":"old","password":"nope"}`); code != 401 && code != 403 {
		t.Errorf("wrong password: %d %s", code, body)
	}
	if code, body := claim("newcomer", `{"username":"old","password":"pw"}`); code != 201 || !strings.Contains(body, `"owner":true`) {
		t.Fatalf("claim: %d %s", code, body)
	}
	// One profile: the owner's.
	if code, body := getAs(t, srv, "newcomer", "/api/v1/profiles"); code != 200 || strings.Count(body, `"id"`) != 1 {
		t.Errorf("profiles: %d %s", code, body)
	}
	// Only visitors: someone with an account can't claim another.
	if code, body := claim("newcomer", `{"username":"old2","password":"pw"}`); code != 409 || !strings.Contains(body, "account-exists") {
		t.Errorf("claim with an account: %d %s", code, body)
	}
	// And an old account comes over once.
	if code, body := claim("someone-else", `{"username":"old","password":"pw"}`); code != 403 || !strings.Contains(body, "claim-not-allowed") {
		t.Errorf("claiming it again: %d %s", code, body)
	}
}
