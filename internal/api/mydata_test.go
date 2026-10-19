package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestExportMyData(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})

	if code, body := post(t, srv, "/api/v1/me/export", `{}`); code != 403 || !strings.Contains(body, "step-up-required") {
		t.Fatalf("export without a fresh login: %d %s", code, body)
	}
	stepUp(t, srv, idp, subject, time.Now())

	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/me/export", strings.NewReader("{}"))
	req.Header.Set("Remote-User", subject)
	req.Header.Set("Content-Type", "application/json")
	res, err := client(srv, subject).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	body := string(b)
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %s", res.StatusCode, res.Header.Get("Content-Disposition"))
	}
	for _, want := range []string{`"account":{"id":"acct_`, `"owner":true`, `"watchHistory":[{`, `"name":"Movie 0"`, `"sessions":[{`} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %s: %s", want, body)
		}
	}
	// Never secrets (rule 52).
	for _, secret := range []string{token, "pin_hash", "jellyfinToken", "of-test"} {
		if strings.Contains(body, secret) {
			t.Errorf("export contains %q", secret)
		}
	}
}

func TestDeleteAccount(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})

	// Another profile can't delete the account.
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	partner := between(body, `"id":"`, `"`)
	post(t, srv, "/api/v1/profiles/"+partner+"/activate", `{}`)
	if code, _ := put(t, srv, "DELETE", "/api/v1/me", ""); code != 403 {
		t.Errorf("non-owner deleted the account: %d", code)
	}
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)

	if code, body := put(t, srv, "DELETE", "/api/v1/me", ""); code != 403 || !strings.Contains(body, "step-up-required") {
		t.Fatalf("delete without a fresh login: %d %s", code, body)
	}
	stepUp(t, srv, idp, subject, time.Now())
	code, body := put(t, srv, "DELETE", "/api/v1/me", "")
	if code != 200 || !strings.Contains(body, `"history":"deleted"`) {
		t.Fatalf("delete: %d %s", code, body)
	}
	// Gone: the next visit finds no account.
	if code, body := getAs(t, srv, subject, "/api/v1/me"); code != 404 || !strings.Contains(body, "account-setup-required") {
		t.Errorf("after deletion: %d %s", code, body)
	}
}

func between(s, start, end string) string {
	_, after, ok := strings.Cut(s, start)
	if !ok {
		return ""
	}
	v, _, _ := strings.Cut(after, end)
	return v
}

// Someone on another profile doesn't know the account's login: their own
// data needs only their own PIN, if they set one.
func TestExportFromAnotherProfile(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	partner := between(body, `"id":"`, `"`)
	post(t, srv, "/api/v1/profiles/"+partner+"/activate", `{}`)

	code, body := post(t, srv, "/api/v1/me/export", `{}`)
	if code != 200 || strings.Contains(body, `"account"`) || strings.Contains(body, `"name":"Main"`) ||
		!strings.Contains(body, `"name":"Partner"`) {
		t.Fatalf("export of an unprotected profile: %d %s", code, body)
	}

	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+partner+"/pin", `{"pin":"2580"}`); code != 204 {
		t.Fatalf("partner sets a PIN: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/me/export", `{}`); code != 403 || !strings.Contains(body, "pin-required") {
		t.Errorf("without the PIN: %d %s", code, body)
	}
	if code, _ := post(t, srv, "/api/v1/me/export", `{"pin":"2580"}`); code != 200 {
		t.Errorf("with the PIN: %d", code)
	}
}

func TestKidsCantExport(t *testing.T) {
	srv, _ := newServer(t)
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","ratingCeiling":"G"}`)
	kid := between(body, `"id":"`, `"`)
	post(t, srv, "/api/v1/profiles/"+kid+"/activate", `{}`)
	if code, body := post(t, srv, "/api/v1/me/export", `{}`); code != 403 || !strings.Contains(body, "owner-only") {
		t.Errorf("kids export: %d %s", code, body)
	}
}
