package admin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/config"
	"github.com/C0PT3R/overfin-gateway/internal/oidc"
)

// memFeatures keeps feature settings in memory, refusing what config would.
type memFeatures struct{ f config.Features }

func (m *memFeatures) Features() config.Features { return m.f }
func (m *memFeatures) SetFeatures(_ context.Context, f config.Features) error {
	for _, p := range f.Plugins.Enable {
		if _, ok := config.KnownPlugins[p]; !ok {
			return io.ErrUnexpectedEOF
		}
	}
	m.f = f
	return nil
}

// fakeIdP issues ID tokens for codes it hands out.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	codes map[string]map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: key, codes: map[string]map[string]any{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
				"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
		case "/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			claims, ok := f.codes[r.FormValue("code")]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
			signed := enc(map[string]string{"alg": "RS256", "kid": "k"}) + "." + enc(claims)
			sum := sha256.Sum256([]byte(signed))
			sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
			json.NewEncoder(w).Encode(map[string]string{"id_token": signed + "." + base64.RawURLEncoding.EncodeToString(sig)})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func newPanel(t *testing.T) (*Panel, *fakeIdP, *memFeatures) {
	idp := newFakeIdP(t)
	c := idp.srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	feats := &memFeatures{f: config.DefaultFeatures()}
	p := &Panel{Features: feats, Group: "overfin-admins",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Provider: &oidc.Provider{Issuer: idp.srv.URL, ClientID: "overfin", ClientSecret: "s",
			RedirectURL: "http://localhost:8081/admin/auth/callback", GroupsClaim: "groups", HTTP: c}}
	return p, idp, feats
}

// browser keeps cookies by hand and makes requests from a private address.
type browser struct {
	h       http.Handler
	cookies map[string]string
}

func (b *browser) do(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:50000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for k, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	b.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	return rec
}

// signIn goes through the whole sign-in, with the given groups.
func signIn(t *testing.T, b *browser, idp *fakeIdP, groups []string) *httptest.ResponseRecorder {
	t.Helper()
	rec := b.do("GET", "/admin/login?return=/admin/", "", nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if q.Get("prompt") != "login" || !strings.Contains(q.Get("scope"), "profile") {
		t.Fatalf("authorize request: %s", loc)
	}
	code := oidc.Random(8)
	now := time.Now()
	idp.codes[code] = map[string]any{"iss": idp.srv.URL, "aud": "overfin", "sub": "me", "nonce": q.Get("nonce"),
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "auth_time": now.Unix(),
		"groups": groups, "preferred_username": "alice"}
	ret := url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
	if rec := b.do("GET", "/admin/auth/callback?"+ret, "", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 200 {
		t.Fatalf("callback: %d", rec.Code)
	}
	return b.do("GET", "/admin/auth/finish?"+ret, "", map[string]string{"Sec-Fetch-Site": "same-origin"})
}

func TestOnlyPrivateAddresses(t *testing.T) {
	p, _, _ := newPanel(t)
	h := p.Handler()
	for _, tc := range []struct {
		peer    string
		headers map[string]string
		ok      bool
	}{
		{"127.0.0.1:1", nil, true},
		{"192.168.1.20:1", nil, true},
		{"172.16.0.2:1", map[string]string{"X-Forwarded-For": "192.168.1.30"}, true},
		{"203.0.113.9:1", nil, false},
		{"172.16.0.2:1", map[string]string{"X-Forwarded-For": "203.0.113.9"}, false},
		{"172.16.0.2:1", map[string]string{"X-Forwarded-For": "192.168.1.1, 198.51.100.4"}, false},
		{"172.16.0.2:1", map[string]string{"Forwarded": `for="[2001:db8::1]:4711"`}, false},
		{"172.16.0.2:1", map[string]string{"X-Real-Ip": "garbage"}, false},
	} {
		req := httptest.NewRequest("GET", "/admin/", nil)
		req.RemoteAddr = tc.peer
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if (rec.Code == 200) != tc.ok {
			t.Errorf("%s %v: %d", tc.peer, tc.headers, rec.Code)
		}
	}
}

func TestSignInAndChange(t *testing.T) {
	p, idp, feats := newPanel(t)
	b := &browser{h: p.Handler(), cookies: map[string]string{}}

	if rec := b.do("GET", "/admin/api/features", "", nil); rec.Code != 401 {
		t.Fatalf("before signing in: %d", rec.Code)
	}
	rec := signIn(t, b, idp, []string{"overfin-admins"})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/api/session", "", nil); !strings.Contains(rec.Body.String(), `"name":"alice"`) {
		t.Errorf("session: %s", rec.Body)
	}

	f := feats.f
	f.Plugins.Enable = []string{"library"}
	body, _ := json.Marshal(f)
	if rec := b.do("PUT", "/admin/api/features", string(body), map[string]string{"Sec-Fetch-Site": "same-origin"}); rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if !feats.f.Plugins.Enabled("library") {
		t.Error("not applied")
	}
	// From another site: refused.
	if rec := b.do("PUT", "/admin/api/features", string(body), map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != 403 {
		t.Errorf("cross-site change: %d", rec.Code)
	}
	// Activity keeps the session going: changes 20 minutes apart, for an
	// hour, need no new sign-in.
	start := time.Now()
	for m := 20; m <= 60; m += 20 {
		p.now = func() time.Time { return start.Add(time.Duration(m) * time.Minute) }
		if rec := b.do("PUT", "/admin/api/features", string(body), nil); rec.Code != 200 {
			t.Fatalf("change after %d minutes of activity: %d %s", m, rec.Code, rec.Body)
		}
	}
	// Half an hour without activity ends it.
	p.now = func() time.Time { return start.Add(95 * time.Minute) }
	if rec := b.do("GET", "/admin/api/features", "", nil); rec.Code != 401 {
		t.Errorf("after 35 idle minutes: %d", rec.Code)
	}
}

func TestOnlyAdmins(t *testing.T) {
	p, idp, _ := newPanel(t)
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	if rec := signIn(t, b, idp, []string{"media-users"}); rec.Code != 403 {
		t.Errorf("non-admin sign-in: %d", rec.Code)
	}
	if rec := b.do("GET", "/admin/api/features", "", nil); rec.Code != 401 {
		t.Errorf("non-admin got in: %d", rec.Code)
	}
}

func TestPanelPage(t *testing.T) {
	p, _, _ := newPanel(t)
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	rec := b.do("GET", "/admin/", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Overfin control panel") {
		t.Errorf("page: %d", rec.Code)
	}
	if rec := b.do("GET", "/admin/panel.js", "", nil); rec.Code != 200 {
		t.Errorf("script: %d", rec.Code)
	}
}

type fakeModules struct{ mux *http.ServeMux }

func (f fakeModules) Tabs() []Tab {
	return []Tab{
		{Name: "hello", Description: "greets", UI: fstest.MapFS{"panel.js": {Data: []byte("export default () => {}")}}},
		{Name: "plain"}, // no tab
	}
}
func (f fakeModules) AdminRoutes() http.Handler { return f.mux }

func TestModuleTabs(t *testing.T) {
	p, idp, _ := newPanel(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/modules/hello/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]int{"visits": 3})
	})
	p.Modules = fakeModules{mux: mux}
	b := &browser{h: p.Handler(), cookies: map[string]string{}}

	// Nothing for strangers.
	for _, path := range []string{"/admin/api/modules", "/admin/api/modules/hello/stats", "/admin/modules/hello/panel.js"} {
		if rec := b.do("GET", path, "", nil); rec.Code != 401 {
			t.Errorf("%s before signing in: %d", path, rec.Code)
		}
	}
	signIn(t, b, idp, []string{"overfin-admins"})
	if body := b.do("GET", "/admin/api/modules", "", nil).Body.String(); !strings.Contains(body, `{"name":"hello","description":"greets","hasTab":true}`) ||
		!strings.Contains(body, `{"name":"plain","hasTab":false}`) {
		t.Errorf("modules: %s", body)
	}
	if rec := b.do("GET", "/admin/api/modules/hello/stats", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"visits":3`) {
		t.Errorf("module admin route: %d %s", rec.Code, rec.Body)
	}
	if rec := b.do("GET", "/admin/modules/hello/panel.js", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "export default") {
		t.Errorf("module tab: %d", rec.Code)
	}
	for _, path := range []string{"/admin/modules/plain/panel.js", "/admin/modules/nope/panel.js", "/admin/api/modules/nope/x"} {
		if rec := b.do("GET", path, "", nil); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

// When the provider refuses the sign-in, the panel says so instead of
// starting over, which would loop on a provider that refuses every time.
func TestProviderRefusalIsShown(t *testing.T) {
	p, _, _ := newPanel(t)
	b := &browser{h: p.Handler(), cookies: map[string]string{}}
	rec := b.do("GET", "/admin/login?return=/admin/", "", nil)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	ret := url.Values{"error": {"invalid_request"}, "state": {loc.Query().Get("state")}}.Encode()
	b.do("GET", "/admin/auth/callback?"+ret, "", map[string]string{"Sec-Fetch-Site": "cross-site"})
	rec = b.do("GET", "/admin/auth/finish?"+ret, "", map[string]string{"Sec-Fetch-Site": "same-origin"})
	if rec.Code != 403 || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Errorf("refused sign-in: %d %q %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
}
