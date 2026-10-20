package api_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/config"
)

// newBrowser is a fresh browser: its own cookies, no redirects followed,
// and no identity headers.
func newBrowser(srv *httptest.Server) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: srv.Client().Transport, Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func visit(t *testing.T, b *http.Client, srv *httptest.Server, path string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

// logIn runs a whole login in browser b as sub, and returns the final
// response.
func logIn(t *testing.T, b *http.Client, srv *httptest.Server, idp *fakeIdP, sub, ret string) *http.Response {
	t.Helper()
	res, _ := visit(t, b, srv, "/api/v1/login?return="+url.QueryEscape(ret), nil)
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, idp.srv.URL+"/authorize?") {
		t.Fatalf("login start: %d %s", res.StatusCode, loc)
	}
	if !strings.Contains(loc, "offline_access") || strings.Contains(loc, "prompt=login") {
		t.Errorf("login request: %s", loc)
	}
	code, state := idp.login(loc, sub, time.Now())
	q := url.Values{"code": {code}, "state": {state}}.Encode()
	if res, page := visit(t, b, srv, "/api/v1/auth/callback?"+q, map[string]string{
		"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}); res.StatusCode != 200 {
		t.Fatalf("callback: %d %s", res.StatusCode, page)
	}
	res, _ = visit(t, b, srv, "/api/v1/auth/finish?"+q, map[string]string{"Sec-Fetch-Site": "same-origin"})
	return res
}

func TestLogin(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true})
	b := newBrowser(srv)

	// Identity headers count for nothing in this mode, whoever sends them.
	if res, _ := visit(t, b, srv, "/api/v1/me", map[string]string{"Remote-User": subject}); res.StatusCode != 401 {
		t.Fatalf("header identity accepted: %d", res.StatusCode)
	}

	// The gate sends a visitor's page to the login, and refuses the rest.
	res, _ := visit(t, b, srv, "/api/v1/gate", map[string]string{"Sec-Fetch-Site": "cross-site",
		"Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document", "X-Forwarded-Uri": "/movies?page=2"})
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/api/v1/login?return=%2Fmovies%3Fpage%3D2" {
		t.Errorf("gate, page: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := visit(t, b, srv, "/api/v1/gate", map[string]string{"Sec-Fetch-Dest": "image"}); res.StatusCode != 401 {
		t.Errorf("gate, image: %d", res.StatusCode)
	}

	res = logIn(t, b, srv, idp, subject, "/movies")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/movies" {
		t.Fatalf("finish: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, body := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 200 || !strings.Contains(body, `"owner":true`) {
		t.Errorf("me after login: %d %s", res.StatusCode, body)
	}
	if res, _ := visit(t, b, srv, "/api/v1/gate", map[string]string{"Sec-Fetch-Dest": "image"}); res.StatusCode != 204 {
		t.Errorf("gate after login: %d", res.StatusCode)
	}

	// Every request here re-checks the login (no recheck interval in
	// tests); once the provider refuses, the login is gone.
	idp.mu.Lock()
	if idp.refreshes == 0 {
		t.Error("the login was never re-checked")
	}
	idp.revoked[subject] = true
	idp.mu.Unlock()
	if res, _ := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 401 {
		t.Errorf("after revocation at the provider: %d", res.StatusCode)
	}
}

func TestLoginLogout(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true})
	b := newBrowser(srv)
	logIn(t, b, srv, idp, subject, "/")
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/logout", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	res, err := b.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	if res, _ := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 401 {
		t.Errorf("after logout: %d", res.StatusCode)
	}
}

// A login answer only completes in the browser that asked for it.
func TestLoginCSRF(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true})
	attacker, victim := newBrowser(srv), newBrowser(srv)

	res, _ := visit(t, attacker, srv, "/api/v1/login?return=/", nil)
	code, state := idp.login(res.Header.Get("Location"), "attacker", time.Now())
	// The attacker lures the victim to the finish address.
	res, _ = visit(t, victim, srv, "/api/v1/auth/finish?"+url.Values{"code": {code}, "state": {state}}.Encode(), nil)
	if res.StatusCode != 404 {
		t.Errorf("victim logged into the attacker's account: %d", res.StatusCode)
	}
	if res, _ := visit(t, victim, srv, "/api/v1/gate", map[string]string{"Sec-Fetch-Dest": "image"}); res.StatusCode != 401 {
		t.Errorf("victim has a login: %d", res.StatusCode)
	}
}

// A login without an account yet can create one.
func TestLoginThenNewAccount(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true})
	b := newBrowser(srv)
	logIn(t, b, srv, idp, "brand-new-user", "/")
	if res, body := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 404 || !strings.Contains(body, "account-setup-required") ||
		!strings.Contains(body, `"claim":"move"`) {
		t.Errorf("me before setup: %d %s", res.StatusCode, body)
	}
}

// A provider without refresh tokens still works: the login just can't be
// re-checked, and gets a short, fixed life instead.
func TestLoginWithoutRefreshTokens(t *testing.T) {
	idp := newFakeIdP(t)
	idp.noRefresh = true
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true})
	b := newBrowser(srv)
	res := logIn(t, b, srv, idp, subject, "/")
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("finish: %d", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == "__Host-overfin-login" && (c.MaxAge <= 0 || c.MaxAge > 12*3600) {
			t.Errorf("login cookie lasts %ds", c.MaxAge)
		}
	}
	if res, _ := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 200 {
		t.Errorf("me: %d", res.StatusCode)
	}
	if idp.refreshes != 0 {
		t.Errorf("%d refreshes without a refresh token", idp.refreshes)
	}
}

// Overfin serves overfin-web beside the API, to logged-in visitors only.
func TestSite(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider(), login: true, site: true})
	b := newBrowser(srv)
	page := map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}

	// Not logged in: a page visit goes to the login and back, even arriving
	// from another site (a link in an email); other requests are refused.
	fromMail := map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "cross-site"}
	if res, _ := visit(t, b, srv, "/", fromMail); res.StatusCode != 302 || res.Header.Get("Location") != "/api/v1/login?return=%2F" {
		t.Errorf("page, logged out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, body := visit(t, b, srv, "/app.js", nil); res.StatusCode != 401 || strings.Contains(body, "import") {
		t.Errorf("file, logged out: %d", res.StatusCode)
	}

	logIn(t, b, srv, idp, subject, "/")
	res, body := visit(t, b, srv, "/", page)
	if res.StatusCode != 200 || !strings.Contains(body, "<title>Overfin</title>") ||
		res.Header.Get("Content-Security-Policy") != config.DefaultCSP {
		t.Errorf("page, logged in: %d %v", res.StatusCode, res.Header)
	}
	// The API is still the API.
	if res, body := visit(t, b, srv, "/api/v1/nothing-here", nil); res.StatusCode != 404 || !strings.Contains(body, "not-found") {
		t.Errorf("unknown API route: %d %s", res.StatusCode, body)
	}
	// Other sites may link to pages, never change anything.
	req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader("{}"))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Content-Type", "application/json")
	if res, err := b.Do(req); err != nil || res.StatusCode != 403 {
		t.Errorf("cross-site POST: %v %v", res.StatusCode, err)
	}
}
