package api_test

import (
	"bytes"
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
	"time"

	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/invites"
	"github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// mailbox keeps the emails Overfin sends.
type mailbox struct {
	mu   sync.Mutex
	sent []email
}

type email struct{ to, subject, body, html string }

func (b *mailbox) SendMessage(_ context.Context, to string, m mail.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, email{to, m.Subject, m.Text, m.HTML})
	return nil
}

func (b *mailbox) all() []email {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]email(nil), b.sent...)
}

// linkIn is an email's link token after #name=.
func linkIn(body, name string) string {
	m := regexp.MustCompile(`#` + name + `=([A-Za-z0-9_-]{43})`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return m[1]
}

// signinPost posts JSON to one of the pages' routes as browser b.
func signinPost(t *testing.T, b *http.Client, srv *httptest.Server, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(raw))
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

// checked is a form's body with its check against scripts solved.
func checked(t *testing.T, srv *httptest.Server, body map[string]any) map[string]any {
	t.Helper()
	for k, v := range solve(t, srv) {
		body[k] = v
	}
	return body
}

// openPage opens one of the pages in French.
func openPage(t *testing.T, b *http.Client, srv *httptest.Server, path string) (*http.Response, string) {
	t.Helper()
	return visit(t, b, srv, path, map[string]string{"Accept-Language": "fr", "Sec-Fetch-Dest": "document"})
}

func TestManagedSignIn(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, site: true})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)

	// The site sends a visitor to the sign-in.
	b := newBrowser(srv)
	res, _ := visit(t, b, srv, "/movies", map[string]string{"Sec-Fetch-Dest": "document"})
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/signin?return=%2Fmovies" {
		t.Fatalf("site, signed out: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, body := openPage(t, b, srv, "/signin?return=%2Fmovies")
	if res.StatusCode != 200 || !strings.Contains(body, "Adresse courriel") || !strings.Contains(body, `href="/reset"`) ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("sign-in page: %d %s", res.StatusCode, body)
	}

	// The check against scripts is needed, and works once.
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"email": "ann@example.org", "password": "a long password 1"}); c != 400 ||
		!strings.Contains(out["detail"].(string), "vérification") {
		t.Errorf("without the check: %d %v", c, out)
	}
	proof := solve(t, srv)
	body1 := map[string]any{"email": "ann@example.org", "password": "wrong password 1", "challenge": proof["challenge"], "answer": proof["answer"]}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", body1); c != 401 || out["type"] != "signin-failed" {
		t.Errorf("wrong password: %d %v", c, out)
	}
	body1["password"] = "a long password 1"
	if c, _ := signinPost(t, b, srv, "/api/v1/signin", body1); c != 400 {
		t.Errorf("the same check twice: %d", c)
	}
	// An unknown email gets the same answer as a wrong password.
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "nobody@example.org", "password": "a long password 1"})); c != 401 ||
		out["type"] != "signin-failed" {
		t.Errorf("unknown email: %d %v", c, out)
	}
	c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "Ann@example.org", "password": "a long password 1", "return": "/movies"}))
	if c != 200 || out["next"] != "/movies" {
		t.Fatalf("right password: %d %v", c, out)
	}
	if res, body := visit(t, b, srv, "/api/v1/me", nil); res.StatusCode != 404 || !strings.Contains(body, "account-setup-required") {
		t.Errorf("signed in, no account yet: %d %s", res.StatusCode, body)
	}
	// Signed in, the sign-in page leads on.
	if res, _ := openPage(t, b, srv, "/signin?return=%2Fmovies"); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/movies" {
		t.Errorf("sign-in page, signed in: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if len(box.all()) != 0 {
		t.Errorf("emails sent: %v", box.all())
	}
}

// Registering with an invitation: an email and a password, a link to
// confirm the address, then the account, shaped by the invitation.
func TestManagedRegistration(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	var tmpl string
	panel := http.NewServeMux()
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m, template: &tmpl, admin: panel,
		modules: []feature.Feature{invites.Feature{}}, settings: map[string]map[string]string{"invites": {"required": "on"}}})
	admin := func(method, path string, body any) map[string]any {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		panel.ServeHTTP(rec, httptest.NewRequest(method, "/admin/api/modules/invites"+path, bytes.NewReader(raw)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	it := admin("POST", "/templates", map[string]any{"name": "Friends", "accountTemplate": tmpl, "accessDays": 0, "validDays": 0, "maxUses": 0})
	link := strings.TrimPrefix(admin("POST", "/invites", map[string]any{"template": it["id"], "label": "Friends"})["link"].(string), "https://media.example.org")
	if !strings.HasPrefix(link, "/invite/") {
		t.Fatalf("link %q", link)
	}

	// Without the invitation's page first, no registering.
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "new@example.org", "password": "a long password 1"})); c != 403 {
		t.Errorf("without an invitation: %d %v", c, out)
	}
	if res, body := openPage(t, b, srv, "/invite/AAAAAAAAAAAAAAAAAAAAAA"); res.StatusCode != 200 || !strings.Contains(body, "n’est pas valide") || strings.Contains(body, `id="register"`) {
		t.Errorf("a made-up invitation: %d %s", res.StatusCode, body)
	}
	res, body := openPage(t, b, srv, link)
	if res.StatusCode != 200 || !strings.Contains(body, "Vous êtes invité à rejoindre Overfin") || !strings.Contains(body, `id="register"`) ||
		!strings.Contains(body, "Au moins 10 caractères, dont un chiffre et un caractère spécial.") {
		t.Fatalf("the invitation's page: %d %s", res.StatusCode, body)
	}
	// The password rules.
	for pw, want := range map[string]string{"short1!": "10 caractères", "no digits here": "chiffre", "nospecial123": "spécial"} {
		if c, out := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "new@example.org", "password": pw})); c != 400 ||
			!strings.Contains(out["detail"].(string), want) {
			t.Errorf("%q: %d %v", pw, c, out)
		}
	}
	if c, out := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "New@example.org", "password": "a long password 1"})); c != 200 || out["sent"] != true {
		t.Fatalf("registering: %d %v", c, out)
	}
	m.Wait()
	mails := box.all()
	if len(mails) != 1 || mails[0].to != "New@example.org" || mails[0].subject != "Confirmez votre adresse" {
		t.Fatalf("emails: %+v", mails)
	}
	token := linkIn(mails[0].body, "verify")
	if token == "" || !strings.Contains(mails[0].body, "https://media.example.org/signin#verify=") {
		t.Fatalf("the link: %s", mails[0].body)
	}
	// Before confirming, the right password only says so.
	other := newBrowser(srv)
	if c, out := signinPost(t, other, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "new@example.org", "password": "a long password 1"})); c != 200 || out["unconfirmed"] != true {
		t.Errorf("unconfirmed: %d %v", c, out)
	}
	// The same email again gets a notice, with the same answer.
	if c, out := signinPost(t, b, srv, "/api/v1/register", checked(t, srv, map[string]any{"email": "new@example.org", "password": "another password 2"})); c != 200 || out["sent"] != true {
		t.Errorf("registering twice: %d %v", c, out)
	}
	m.Wait()
	if mails := box.all(); len(mails) != 2 || mails[1].subject != "Vous avez déjà un compte" {
		t.Errorf("notice: %+v", mails)
	}
	// The link, opened in another browser: confirmed, signed in, and the
	// account is made by the invitation.
	c, out := signinPost(t, other, srv, "/api/v1/signin/verify", map[string]any{"link": token})
	if c != 200 || out["next"] != "/" {
		t.Fatalf("confirming: %d %v", c, out)
	}
	if c, _ := signinPost(t, other, srv, "/api/v1/signin/verify", map[string]any{"link": token}); c != 401 {
		t.Errorf("the link twice: %d", c)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/me", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resMe, err := other.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resMe.Body.Close()
	if resMe.StatusCode != 201 {
		t.Fatalf("the account: %d", resMe.StatusCode)
	}
	if list := admin("GET", "/invites", nil)["items"].([]any); list[0].(map[string]any)["uses"] != 1.0 {
		t.Errorf("the invitation's uses: %v", list)
	}
}

// Without email, nobody registers, and there's no forgotten-password page:
// an admin makes accounts, and sets passwords.
func TestManagedWithoutMail(t *testing.T) {
	var tmpl string
	panel := http.NewServeMux()
	srv, _ := newServerWith(t, options{managed: true, template: &tmpl, admin: panel, modules: []feature.Feature{invites.Feature{}}})
	b := newBrowser(srv)
	if res, body := openPage(t, b, srv, "/signin"); res.StatusCode != 200 || strings.Contains(body, `href="/reset"`) ||
		!strings.Contains(body, "Demandez à un administrateur") {
		t.Errorf("sign-in page: %d %s", res.StatusCode, body)
	}
	if res, _ := openPage(t, b, srv, "/reset"); res.StatusCode != 404 {
		t.Errorf("reset page: %d", res.StatusCode)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/reset", checked(t, srv, map[string]any{"email": "ann@example.org"})); c != 400 {
		t.Errorf("asking a link: %d %v", c, out)
	}
}

// An account an admin made asks for a password of one's own first.
func TestManagedMustChange(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, site: true})
	ctx := context.Background()
	bob, _ := st.CreateUser(ctx, "bob@example.org", false)
	hash, _ := password.Hash("from the admin 1!")
	st.SetPassword(ctx, bob.Subject, hash)
	st.SetMustChange(ctx, bob.Subject, true)

	b := newBrowser(srv)
	c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "bob@example.org", "password": "from the admin 1!", "return": "/movies"}))
	if c != 200 || out["next"] != "/signin?return=%2Fmovies" {
		t.Fatalf("signing in: %d %v", c, out)
	}
	// Not in yet: the site sends Bob back to choose it.
	if res, _ := visit(t, b, srv, "/movies", map[string]string{"Sec-Fetch-Dest": "document"}); res.StatusCode != http.StatusFound {
		t.Errorf("the site, before: %d", res.StatusCode)
	}
	if res, body := openPage(t, b, srv, "/signin?return=%2Fmovies"); res.StatusCode != 200 || !strings.Contains(body, `id="change"`) {
		t.Errorf("the page: %d %s", res.StatusCode, body)
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/signin/password", map[string]any{"password": "short", "return": "/movies"}); c != 400 {
		t.Errorf("a short one: %d", c)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin/password", map[string]any{"password": "my own one, 2026", "return": "/movies"}); c != 200 || out["next"] != "/movies" {
		t.Fatalf("choosing it: %d %v", c, out)
	}
	if res, _ := visit(t, b, srv, "/movies", map[string]string{"Sec-Fetch-Dest": "document"}); res.StatusCode != 200 {
		t.Errorf("the site, after: %d", res.StatusCode)
	}
}

// A forgotten password: a link by email, a new password, every other login
// ended.
func TestManagedReset(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	elsewhere, _, _ := st.CreateLogin(ctx, ann.Subject, nil, "", time.Hour)

	b := newBrowser(srv)
	if res, body := openPage(t, b, srv, "/reset"); res.StatusCode != 200 || !strings.Contains(body, `id="reset"`) {
		t.Fatalf("reset page: %d %s", res.StatusCode, body)
	}
	for _, who := range []string{"nobody@example.org", "ann@example.org"} {
		if c, out := signinPost(t, b, srv, "/api/v1/reset", checked(t, srv, map[string]any{"email": who})); c != 200 || out["sent"] != true {
			t.Errorf("%s: %d %v", who, c, out)
		}
	}
	m.Wait()
	mails := box.all()
	if len(mails) != 1 || mails[0].to != "ann@example.org" || mails[0].subject != "Choisissez un nouveau mot de passe" {
		t.Fatalf("emails: %+v", mails)
	}
	token := linkIn(mails[0].body, "reset")
	if c, _ := signinPost(t, b, srv, "/api/v1/reset/password", map[string]any{"link": token, "password": "short"}); c != 400 {
		t.Errorf("a short one: %d", c)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/reset/password", map[string]any{"link": token, "password": "a new one, 2026"}); c != 200 || out["next"] != "/" {
		t.Fatalf("the new password: %d %v", c, out)
	}
	if _, err := st.LoginByToken(ctx, elsewhere.ID); err == nil {
		t.Error("another login still works")
	}
	if u, _ := st.UserByEmail(ctx, "ann@example.org"); !password.Check(u.PasswordHash, "a new one, 2026") {
		t.Error("the password wasn't changed")
	}
	if c, _ := signinPost(t, b, srv, "/api/v1/reset/password", map[string]any{"link": token, "password": "a new one, 2027"}); c != 401 {
		t.Errorf("the link twice: %d", c)
	}
}

// Someone signed in confirms it's them with their password, then changes
// it; it ends their other logins.
func TestManagedPassword(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, freshFor: time.Minute})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("signing in: %d %v", c, out)
	}
	other := newBrowser(srv)
	signinPost(t, other, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"}))

	put := func(c *http.Client, body string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/me/password", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := put(b, `{"password":"short"}`); code != 400 {
		t.Errorf("a short one: %d", code)
	}
	if code := put(b, `{"password":"a new one, 2026"}`); code != 200 {
		t.Fatalf("a new one: %d", code)
	}
	if res, _ := visit(t, other, srv, "/api/v1/me/signin", nil); res.StatusCode != 401 {
		t.Errorf("the other login: %d", res.StatusCode)
	}
	// Confirming it's them takes the password alone, and no check.
	if res, body := openPage(t, b, srv, "/signin?fresh=1"); res.StatusCode != 200 || !strings.Contains(body, `id="fresh"`) {
		t.Errorf("confirm page: %d %s", res.StatusCode, body)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/signin", map[string]any{"password": "a new one, 2026", "fresh": true, "return": "/account"}); c != 200 || out["next"] != "/account" {
		t.Errorf("confirming: %d %v", c, out)
	}
}

// The password is the whole account's: another profile on a browser just
// signed in can neither change it nor read the email (rule 25).
func TestManagedPasswordOwnerOnly(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, freshFor: time.Minute})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", false)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	acct, owner, _ := st.CreateAccount(ctx, ann.Subject, store.NewOwner{Name: "Ann"})
	kid, err := st.AddProfile(ctx, acct.ID, store.NewProfile{Name: "Kid", Kind: "kids", MovieCeiling: "G", SeriesCeiling: "TV-Y"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("signing in: %d %v", c, out)
	}
	put := func() int {
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/me/password", strings.NewReader(`{"password":"the kid's choice, 2026"}`))
		req.Header.Set("Content-Type", "application/json")
		res, err := b.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if c, out := signinPost(t, b, srv, "/api/v1/profiles/"+kid.ID+"/activate", map[string]any{}); c != 200 {
		t.Fatalf("switching to the kid: %d %v", c, out)
	}
	if code := put(); code != 403 {
		t.Errorf("the kid's new password: %d", code)
	}
	if res, _ := visit(t, b, srv, "/api/v1/me/signin", nil); res.StatusCode != 403 {
		t.Errorf("the email, to the kid: %d", res.StatusCode)
	}
	if u, _ := st.UserByEmail(ctx, "ann@example.org"); !password.Check(u.PasswordHash, "a long password 1") {
		t.Fatal("the password was changed")
	}
	if c, out := signinPost(t, b, srv, "/api/v1/profiles/"+owner.ID+"/activate", map[string]any{}); c != 200 {
		t.Fatalf("back to the owner: %d %v", c, out)
	}
	if code := put(); code != 200 {
		t.Errorf("the owner's new password: %d", code)
	}
}

// An admin whose account is disabled loses the control panel at once
// (rule 16), even signed in and fresh.
func TestSiteAdminDisabledAccount(t *testing.T) {
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, store: &st, signIn: &m, freshFor: time.Minute})
	ctx := context.Background()
	boss, _ := st.CreateUser(ctx, "boss@example.org", true)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, boss.Subject, hash)
	acct, _, _ := st.CreateAccount(ctx, boss.Subject, store.NewOwner{Name: "Boss"})
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "boss@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("signing in: %d %v", c, out)
	}
	ask := func() (string, bool, bool) {
		r := httptest.NewRequest("GET", srv.URL+"/admin/login", nil)
		u, _ := url.Parse(srv.URL)
		for _, c := range b.Jar.Cookies(u) {
			r.AddCookie(c)
		}
		return m.SiteAdmin(r)
	}
	if email, admin, fresh := ask(); email != "boss@example.org" || !admin || !fresh {
		t.Fatalf("before: %q %v %v", email, admin, fresh)
	}
	if err := st.SetAccountDisabled(ctx, acct.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, admin, _ := ask(); admin {
		t.Error("a disabled account is still an admin")
	}
}

// Only wrong passwords count towards an email's limit: someone signing in
// often isn't locked out, and the right password starts the count over.
// (Each server allows 20 tries a minute from one address.)
func TestSigninLimitCountsWrongPasswords(t *testing.T) {
	server := func() func(pw string) int {
		var st *store.Store
		srv, _ := newServerWith(t, options{managed: true, store: &st})
		ctx := context.Background()
		ann, _ := st.CreateUser(ctx, "ann@example.org", false)
		hash, _ := password.Hash("a long password 1")
		st.SetPassword(ctx, ann.Subject, hash)
		return func(pw string) int {
			c, _ := signinPost(t, newBrowser(srv), srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": pw}))
			return c
		}
	}
	try := server()
	for i := range 11 {
		if c := try("a long password 1"); c != 200 {
			t.Fatalf("sign-in %d: %d", i+1, c)
		}
	}
	try = server()
	for round := range 2 {
		for range 9 {
			try("a wrong one")
		}
		if c := try("a long password 1"); c != 200 {
			t.Fatalf("round %d, after 9 wrong: %d", round+1, c)
		}
	}
}

// An email goes out in the account's language, whatever the browser's.
func TestSigninEmailLanguage(t *testing.T) {
	box := &mailbox{}
	var st *store.Store
	var m *api.Managed
	srv, _ := newServerWith(t, options{managed: true, mailbox: box, store: &st, signIn: &m})
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "ann@example.org", false)
	acct, _, _ := st.CreateAccount(ctx, u.Subject, store.NewOwner{Name: "Ann"})
	st.SetAccountLanguage(ctx, acct.ID, "en-US")

	b := newBrowser(srv)
	// The browser asks in French; Ann's account is in English.
	if c, out := signinPost(t, b, srv, "/api/v1/reset", checked(t, srv, map[string]any{"email": "ann@example.org"})); c != 200 || out["sent"] != true {
		t.Fatalf("asking a link: %d %v", c, out)
	}
	m.Wait()
	if mails := box.all(); len(mails) != 1 || mails[0].subject != "Choose a new password" {
		t.Errorf("emails: %+v", mails)
	}
}

// "Download my data" has the email the account signs in with (rule 52).
func TestManagedExportHasEmail(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{managed: true, store: &st, freshFor: time.Minute})
	ctx := context.Background()
	ann, _ := st.CreateUser(ctx, "ann@example.org", true)
	hash, _ := password.Hash("a long password 1")
	st.SetPassword(ctx, ann.Subject, hash)
	b := newBrowser(srv)
	if c, out := signinPost(t, b, srv, "/api/v1/signin", checked(t, srv, map[string]any{"email": "ann@example.org", "password": "a long password 1"})); c != 200 {
		t.Fatalf("signing in: %d %v", c, out)
	}
	if c, out := signinPost(t, b, srv, "/api/v1/me", map[string]any{}); c != 200 && c != 201 {
		t.Fatalf("creating the account: %d %v", c, out)
	}
	signinPost(t, b, srv, "/api/v1/signin", map[string]any{"password": "a long password 1", "fresh": true})
	c, out := signinPost(t, b, srv, "/api/v1/me/export", map[string]any{})
	account, _ := out["account"].(map[string]any)
	if c != 200 || account["email"] != "ann@example.org" || account["admin"] != true || account["limits"] == nil {
		t.Fatalf("export: %d %v", c, out)
	}
}
