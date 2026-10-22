package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// mailbox keeps the emails Overfin sends.
type mailbox struct {
	mu   sync.Mutex
	sent []email
}

type email struct{ to, subject, body string }

func (b *mailbox) Send(_ context.Context, to, subject, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, email{to, subject, body})
	return nil
}

func (b *mailbox) all() []email {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]email(nil), b.sent...)
}

var (
	codePattern = regexp.MustCompile(`\b\d{6}\b`)
	linkPattern = regexp.MustCompile(`#link=([A-Za-z0-9_-]{43})`)
)

// signinPost posts JSON to a sign-in route as browser b.
func signinPost(t *testing.T, b *http.Client, srv *httptest.Server, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Accept-Language", "fr-CA,fr;q=0.9,en;q=0.8")
	res, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	data, _ := io.ReadAll(res.Body)
	json.Unmarshal(data, &out)
	return res.StatusCode, out
}

// signinPage opens the sign-in page, which gives the browser its binding.
func signinPage(t *testing.T, b *http.Client, srv *httptest.Server) string {
	t.Helper()
	res, page := visit(t, b, srv, "/api/v1/signin?return=%2Fmovies", map[string]string{"Accept-Language": "fr"})
	if res.StatusCode != 200 || !strings.Contains(page, "Adresse courriel") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("sign-in page: %d %s", res.StatusCode, page)
	}
	return page
}

func TestManagedSignIn(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m, site: true})
	ctx := context.Background()
	ann, err := st.CreateUser(ctx, "ann@example.org", false)
	if err != nil {
		t.Fatal(err)
	}

	// The site sends a visitor to the sign-in.
	b := newBrowser(srv)
	res, _ := visit(t, b, srv, "/movies", map[string]string{"Sec-Fetch-Dest": "document"})
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/api/v1/signin?return=%2Fmovies" {
		t.Fatalf("site, signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	signinPage(t, b, srv)

	// A code for Ann, by email, in her language.
	if code, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "Ann@example.org"}); code != 200 || out["sent"] != true {
		t.Fatalf("asking a code: %d %v", code, out)
	}
	m.Wait()
	mails := box.all()
	if len(mails) != 1 || mails[0].to != "Ann@example.org" || !strings.Contains(mails[0].subject, "code de connexion") {
		t.Fatalf("emails: %+v", mails)
	}
	code := codePattern.FindString(mails[0].subject)
	if code == "" || !strings.Contains(mails[0].body, "https://media.example.org/api/v1/signin#link=") {
		t.Fatalf("email: %+v", mails[0])
	}

	// The code works in this browser only, and a wrong one counts.
	other := newBrowser(srv)
	signinPage(t, other, srv)
	if c, out := signinPost(t, other, srv, "/api/v1/signin/code", map[string]any{"email": "ann@example.org", "code": code}); c != 401 {
		t.Errorf("code from another browser: %d %v", c, out)
	}
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin/code", map[string]any{"email": "ann@example.org", "code": wrong}); c != 401 ||
		out["type"] != "signin-failed" || !strings.Contains(out["detail"].(string), "code") {
		t.Errorf("wrong code: %d %v", c, out)
	}
	c, out := signinPost(t, b, srv, "/api/v1/signin/code", map[string]any{"email": "ann@example.org", "code": code[:3] + " " + code[3:], "return": "/movies"})
	if c != 200 || out["next"] != "/movies" {
		t.Fatalf("right code: %d %v", c, out)
	}
	// Signed in: Ann has no account yet, and makes one.
	if res, body := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 404 || !strings.Contains(body, "account-setup-required") {
		t.Errorf("signed in, no account yet: %d %s", res.StatusCode, body)
	}
	// Used once.
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/code", map[string]any{"email": "ann@example.org", "code": code}); c != 401 {
		t.Errorf("code used twice: %d", c)
	}

	// Strangers get the same answer, and no email.
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "nobody@example.org"}); c != 200 || out["sent"] != true {
		t.Errorf("stranger: %d %v", c, out)
	}
	m.Wait()
	if n := len(box.all()); n != 1 {
		t.Errorf("an email went to a stranger: %d emails", n)
	}
	// Five codes an hour for an email, whoever has it.
	for i := 0; i < 4; i++ {
		signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "nobody@example.org"})
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "nobody@example.org"}); c != 429 {
		t.Errorf("sixth code: %d %v", c, out)
	}

	// The link works in any browser, once.
	signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "ann@example.org"})
	m.Wait()
	mails = box.all()
	link := linkPattern.FindStringSubmatch(mails[len(mails)-1].body)
	phone := newBrowser(srv)
	if c, out := signinPost(t, phone, srv, "/api/v1/signin/link", map[string]any{"link": link[1]}); c != 200 || out["next"] != "/" {
		t.Errorf("link: %d %v", c, out)
	}
	if res, _ := visit(t, phone, srv, "/api/v1/me", nil); res.StatusCode != 404 {
		t.Errorf("signed in by link: %d", res.StatusCode)
	}
	if c, _ := signinPost(t, newBrowser(srv), srv, "/api/v1/signin/link", map[string]any{"link": link[1]}); c != 401 {
		t.Errorf("link used twice: %d", c)
	}

	// Logging out ends the login.
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if res, err := phone.Do(req); err != nil || res.StatusCode != 200 {
		t.Fatalf("logout: %v %v", res, err)
	}
	if res, _ := visit(t, phone, srv, "/api/v1/me", nil); res.StatusCode != 401 {
		t.Errorf("logged out, still in: %d", res.StatusCode)
	}

	// A password: wrong ones and unknown emails get the same answer.
	hash, _ := password.Hash("correct horse battery")
	st.SetPassword(ctx, ann.Subject, hash)
	pc := newBrowser(srv)
	_, wrongPw := signinPost(t, pc, srv, "/api/v1/signin", map[string]any{"email": "ann@example.org", "password": "wrong password!"})
	_, unknown := signinPost(t, pc, srv, "/api/v1/signin", map[string]any{"email": "nobody@example.org", "password": "wrong password!"})
	if wrongPw["type"] != "signin-failed" || wrongPw["detail"] != unknown["detail"] {
		t.Errorf("wrong password %v, unknown email %v", wrongPw, unknown)
	}
	if c, out := signinPost(t, pc, srv, "/api/v1/signin", map[string]any{"email": "ann@example.org", "password": "correct horse battery"}); c != 200 {
		t.Fatalf("password: %d %v", c, out)
	}

	// The account, then deleting it: that needs a fresh login.
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/me", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if res, err := pc.Do(req); err != nil || res.StatusCode != 201 {
		t.Fatalf("making the account: %v %v", res, err)
	}
	del, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/me", nil)
	res, err = pc.Do(del)
	var p map[string]any
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if err != nil || res.StatusCode != 403 || p["stepUpUrl"] != "/api/v1/step-up" {
		t.Fatalf("delete without a fresh login: %d %v", res.StatusCode, p)
	}
	res, _ = visit(t, pc, srv, "/api/v1/step-up?return=%2Faccount", nil)
	if loc, _ := url.Parse(res.Header.Get("Location")); res.StatusCode != 303 || loc.Path != "/api/v1/signin" || loc.Query().Get("fresh") != "1" {
		t.Fatalf("step-up: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, page := visit(t, pc, srv, res.Header.Get("Location"), nil); res.StatusCode != 200 || !strings.Contains(page, "ann@example.org") {
		t.Errorf("confirming, the page doesn't say who: %s", page)
	}
	if c, out := signinPost(t, pc, srv, "/api/v1/signin", map[string]any{"fresh": true, "password": "correct horse battery", "return": "/account"}); c != 200 || out["next"] != "/account" {
		t.Fatalf("fresh login: %d %v", c, out)
	}
	if res, err := pc.Do(del); err != nil || res.StatusCode != 200 {
		t.Fatalf("delete after a fresh login: %v %v", res, err)
	}
	// Everything goes: her email, password and logins in every browser.
	if _, err := st.UserByEmail(ctx, "ann@example.org"); err != store.ErrNotFound {
		t.Errorf("Ann's email after deleting her account: %v", err)
	}
	if res, _ := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 401 {
		t.Errorf("account deleted, still in: %d", res.StatusCode)
	}

	// Disabled: out at once, everywhere.
	cy, _ := st.CreateUser(ctx, "cy@example.org", false)
	st.SetPassword(ctx, cy.Subject, hash)
	cyb := newBrowser(srv)
	if c, out := signinPost(t, cyb, srv, "/api/v1/signin", map[string]any{"email": "cy@example.org", "password": "correct horse battery"}); c != 200 {
		t.Fatalf("Cy: %d %v", c, out)
	}
	st.SetUserDisabled(ctx, cy.Subject, true)
	if res, _ := visit(t, cyb, srv, "/api/v1/me", nil); res.StatusCode != 401 {
		t.Errorf("disabled, still in: %d", res.StatusCode)
	}
	if c, _ := signinPost(t, cyb, srv, "/api/v1/signin", map[string]any{"email": "cy@example.org", "password": "correct horse battery"}); c != 401 {
		t.Errorf("disabled, signs in: %d", c)
	}
}

// Without email, people sign in with a password, and asking for a code
// says why it can't be.
func TestManagedSignInWithoutMail(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st})
	b := newBrowser(srv)
	page := signinPage(t, b, srv)
	if !strings.Contains(page, `autocomplete="current-password" required`) {
		t.Errorf("the password isn't required: %s", page)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "ann@example.org"}); c != 400 ||
		!strings.Contains(out["detail"].(string), "mot de passe") {
		t.Errorf("code without mail: %d %v", c, out)
	}
}

// A newcomer signs in with an invitation, and only with one.
func TestManagedSignInInvited(t *testing.T) {
	box := &mailbox{}
	var tmpl string
	var m *api.Managed
	panel := http.NewServeMux()
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, signIn: &m, template: &tmpl, admin: panel,
		modules: []feature.Feature{invites.Feature{}}, settings: map[string]map[string]string{"invites": {"required": "on"}}})
	admin := func(path string, body any) map[string]any {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		panel.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/api/modules/invites"+path, strings.NewReader(string(raw))))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	it := admin("/templates", map[string]any{"name": "Friends", "accountTemplate": tmpl, "accessDays": 30, "validDays": 7, "maxUses": 1})
	link, _ := admin("/invites", map[string]any{"template": it["id"]})["link"].(string)

	// Without the invitation: no email.
	b := newBrowser(srv)
	signinPage(t, b, srv)
	signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "bob@example.org"})
	m.Wait()
	if n := len(box.all()); n != 0 {
		t.Fatalf("emailed without an invitation: %d", n)
	}
	// With it, in the same browser.
	if res, _ := visit(t, b, srv, link, nil); res.StatusCode != 303 {
		t.Fatalf("invitation: %d", res.StatusCode)
	}
	signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "bob@example.org"})
	m.Wait()
	mails := box.all()
	if len(mails) != 1 {
		t.Fatalf("emails: %d", len(mails))
	}
	code := codePattern.FindString(mails[0].subject)
	if c, out := signinPost(t, b, srv, "/api/v1/signin/code", map[string]any{"email": "bob@example.org", "code": code}); c != 200 {
		t.Fatalf("newcomer's code: %d %v", c, out)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/me", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if res, err := b.Do(req); err != nil || res.StatusCode != 201 {
		t.Fatalf("newcomer's account: %v %v", res, err)
	}
}
