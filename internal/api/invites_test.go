package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
)

// The invites module: a link from the panel opens the way to an account,
// once, and accounts are only by invitation.
func TestInvites(t *testing.T) {
	var tmpl string
	panel := http.NewServeMux()
	srv, fake := newServerWith(t, options{modules: []feature.Feature{invites.Feature{}}, template: &tmpl, admin: panel,
		settings: map[string]map[string]string{"invites": {"required": "on"}}, site: true})
	admin := func(method, path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		panel.ServeHTTP(rec, httptest.NewRequest(method, "/admin/api/modules/invites"+path, bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	// as asks as someone, with an invitation cookie when given one.
	as := func(who, method, path, body string, cookie *http.Cookie) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Remote-User", who)
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res, err := client(srv, who).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	// open follows an invitation link, as someone not logged in yet.
	noFollow := &http.Client{Transport: srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	open := func(link string) *http.Response {
		res, err := noFollow.Get(srv.URL + link)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	// Without an invitation, no account.
	if code, body := as("stranger", "POST", "/api/v1/me", "{}", nil); code != 403 || !strings.Contains(body, "invitation-required") {
		t.Errorf("no invitation: %d %s", code, body)
	}
	// Bringing a previous account over needs none: it's proof enough. (The
	// old account gets an ID of its own in the fake server.)
	fake.users = map[string]string{"old": fmt.Sprintf("%032x", 0xe0001)}
	if code, body := as("oldtimer", "POST", "/api/v1/me/claim", `{"username":"old","password":"pw"}`, nil); code != 201 {
		t.Errorf("claim without an invitation: %d %s", code, body)
	}

	if code, out := admin("POST", "/templates", map[string]any{"name": "Friends", "accountTemplate": "nope",
		"accessDays": 30, "validDays": 7, "maxUses": 1}); code != 400 {
		t.Errorf("unknown account template: %d %v", code, out)
	}
	code, it := admin("POST", "/templates", map[string]any{"name": "Friends", "accountTemplate": tmpl,
		"accessDays": 30, "validDays": 7, "maxUses": 1})
	if code != 200 {
		t.Fatalf("invite template: %d %v", code, it)
	}
	code, made := admin("POST", "/invites", map[string]any{"template": it["id"], "label": "Bob"})
	full, _ := made["link"].(string)
	// A short link on the site, Overfin serving it.
	link := strings.TrimPrefix(full, "https://media.example.org")
	if code != 200 || !strings.HasPrefix(full, "https://media.example.org/invite/") {
		t.Fatalf("invitation: %d %v", code, made)
	}

	// The link leaves a cookie and leads to the site.
	res := open(link)
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "__Host-overfin-invite" {
			cookie = c
		}
	}
	if res.StatusCode != 303 || res.Header.Get("Location") != "/" || cookie == nil || !cookie.HttpOnly || !cookie.Secure {
		t.Fatalf("opening the link: %d %v", res.StatusCode, res.Header)
	}
	if res := open("/invite/AAAAAAAAAAAAAAAAAAAAAA"); res.StatusCode != 404 {
		t.Errorf("a made-up code: %d", res.StatusCode)
	}

	if code, body := as("bob", "POST", "/api/v1/me", "{}", cookie); code != 201 {
		t.Fatalf("with the invitation: %d %s", code, body)
	}
	// Used once: it works for nobody else, and its link no longer opens.
	if code, body := as("eve", "POST", "/api/v1/me", "{}", cookie); code != 403 || !strings.Contains(body, "invitation-required") {
		t.Errorf("reusing it: %d %s", code, body)
	}
	if res := open(link); res.StatusCode != 404 {
		t.Errorf("used link: %d", res.StatusCode)
	}
	_, list := admin("GET", "/invites", nil)
	items, _ := list["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["uses"] != 1.0 || items[0].(map[string]any)["label"] != "Bob" {
		t.Errorf("invitations: %v", list)
	}
}

// With an identity provider that makes sign-up links, an invitation leads
// there: the same link each time, for the uses left until it expires; once
// it's used up, to the site.
func TestInviteSignUpLinks(t *testing.T) {
	type call struct {
		previous string
		uses     int
		days     int
	}
	var calls []call
	usedUp := false
	signUp := func(_ context.Context, previous string, uses int, until time.Time) (string, string, error) {
		calls = append(calls, call{previous, uses, int(time.Until(until).Hours()+1) / 24})
		switch {
		case previous == "":
			return "https://auth.example.org/st/tok1", "t1", nil
		case usedUp:
			return "", previous, nil
		}
		return "https://auth.example.org/st/tok1", previous, nil
	}
	var tmpl string
	panel := http.NewServeMux()
	srv, _ := newServerWith(t, options{modules: []feature.Feature{invites.Feature{}}, template: &tmpl, admin: panel, signUp: signUp})
	admin := func(method, path string, body any) map[string]any {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		panel.ServeHTTP(rec, httptest.NewRequest(method, "/admin/api/modules/invites"+path, bytes.NewReader(b)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	it := admin("POST", "/templates", map[string]any{"name": "Family", "accountTemplate": tmpl,
		"accessDays": 0, "validDays": 10, "maxUses": 3})
	full, _ := admin("POST", "/invites", map[string]any{"template": it["id"], "label": "Them"})["link"].(string)
	link := strings.TrimPrefix(full, "https://media.example.org")
	noFollow := &http.Client{Transport: srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	open := func() string {
		res, err := noFollow.Get(srv.URL + link)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Location")
	}
	if to := open(); to != "https://auth.example.org/st/tok1" {
		t.Errorf("first opening: to %q", to)
	}
	if to := open(); to != "https://auth.example.org/st/tok1" {
		t.Errorf("second opening: to %q", to)
	}
	usedUp = true
	if to := open(); to != "/" {
		t.Errorf("used up: to %q", to)
	}
	want := []call{{"", 3, 10}, {"t1", 3, 10}, {"t1", 3, 10}}
	if fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Errorf("calls %v, want %v", calls, want)
	}
}

// With accounts only by invitation, the site's admins still get theirs.
func TestInvitesAdminsNeedNone(t *testing.T) {
	srv, _ := newServerWith(t, options{modules: []feature.Feature{invites.Feature{}}, adminGroup: "site-admins",
		settings: map[string]map[string]string{"invites": {"required": "on"}}})
	as := func(who, groups string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/me", strings.NewReader("{}"))
		req.Header.Set("Remote-User", who)
		req.Header.Set("Remote-Groups", groups)
		req.Header.Set("Content-Type", "application/json")
		res, err := client(srv, who).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := as("someone", "friends"); code != 403 || !strings.Contains(body, "invitation-required") {
		t.Errorf("not an admin: %d %s", code, body)
	}
	if code, body := as("boss", "friends|site-admins"); code != 201 {
		t.Errorf("an admin: %d %s", code, body)
	}
}
