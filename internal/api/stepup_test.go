package api_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin/internal/oidc"
)

// fakeIdP plays the identity provider: each code it hands out maps to the
// claims of the ID token it will return for it.
type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	mu        sync.Mutex
	codes     map[string]map[string]any
	refresh   map[string]string // refresh token -> subject
	revoked   map[string]bool   // subjects logged out at the provider
	refreshes int
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := &fakeIdP{key: key, codes: map[string]map[string]any{}, refresh: map[string]string{}, revoked: map[string]bool{}}
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
			f.mu.Lock()
			defer f.mu.Unlock()
			if r.FormValue("grant_type") == "refresh_token" {
				sub, ok := f.refresh[r.FormValue("refresh_token")]
				delete(f.refresh, r.FormValue("refresh_token")) // rotated: single use
				if !ok || f.revoked[sub] {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":"invalid_grant"}`)
					return
				}
				f.refreshes++
				next := oidc.Random(16)
				f.refresh[next] = sub
				json.NewEncoder(w).Encode(map[string]string{"refresh_token": next})
				return
			}
			claims, ok := f.codes[r.FormValue("code")]
			delete(f.codes, r.FormValue("code"))
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			rt := oidc.Random(16)
			f.refresh[rt] = claims["sub"].(string)
			json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(claims), "refresh_token": rt})
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	signed := enc(map[string]string{"alg": "RS256", "kid": "k"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) provider() *oidc.Provider {
	c := f.srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &oidc.Provider{Issuer: f.srv.URL, ClientID: "overfin", ClientSecret: "s",
		RedirectURL: "https://example.org/api/v1/auth/callback", HTTP: c}
}

// login answers an authorization request the way the provider would after
// the user logs in, and returns the code.
func (f *fakeIdP) login(authURL, sub string, authTime time.Time) (code, state string) {
	u, _ := url.Parse(authURL)
	q := u.Query()
	code = oidc.Random(16)
	now := time.Now()
	f.mu.Lock()
	f.codes[code] = map[string]any{"iss": f.srv.URL, "aud": "overfin", "sub": sub, "nonce": q.Get("nonce"),
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "auth_time": authTime.Unix()}
	f.mu.Unlock()
	return code, q.Get("state")
}

// browse makes a GET as the test subject without following redirects,
// sharing the subject's cookies.
func browse(t *testing.T, srv *httptest.Server, path string, headers map[string]string) *http.Response {
	t.Helper()
	jarred := client(srv, subject)
	c := &http.Client{Transport: jarred.Transport, Jar: jarred.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// stepUp runs a whole step-up and returns where the browser ends up.
func stepUp(t *testing.T, srv *httptest.Server, idp *fakeIdP, sub string, authTime time.Time) *http.Response {
	t.Helper()
	res := browse(t, srv, "/api/v1/step-up?return=/settings/profiles", nil)
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(res.Header.Get("Location"), idp.srv.URL+"/authorize?") {
		t.Fatalf("start: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	code, state := idp.login(res.Header.Get("Location"), sub, authTime)

	// The provider sends the browser back, cross-site.
	res = browse(t, srv, "/api/v1/auth/callback?"+url.Values{"code": {code}, "state": {state}}.Encode(),
		map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"})
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(page), `content="0;url=/api/v1/auth/finish?`) {
		t.Fatalf("callback: %d %s", res.StatusCode, page)
	}
	return browse(t, srv, "/api/v1/auth/finish?"+url.Values{"code": {code}, "state": {state}}.Encode(),
		map[string]string{"Sec-Fetch-Site": "same-origin"})
}

func TestStepUp(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})
	owner := ownerID(t, srv)
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+owner+"/pin", `{"pin":"1234"}`); code != 204 {
		t.Fatalf("set PIN: %d %s", code, body)
	}

	code, body := put(t, srv, "DELETE", "/api/v1/profiles/"+owner+"/pin", "")
	if code != 403 || !strings.Contains(body, `"stepUpUrl":"/api/v1/step-up"`) || !strings.Contains(body, `"stepUp":"login"`) {
		t.Fatalf("before step-up: %d %s", code, body)
	}

	// A login by someone else, or an old login, doesn't count.
	if res := stepUp(t, srv, idp, "someone-else", time.Now()); res.StatusCode != 403 {
		t.Errorf("another user's login: %d", res.StatusCode)
	}
	if res := stepUp(t, srv, idp, subject, time.Now().Add(-time.Hour)); res.StatusCode != 403 {
		t.Errorf("an old login: %d", res.StatusCode)
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/profiles/"+owner+"/pin", ""); code != 403 {
		t.Fatalf("failed step-ups made the session fresh: %d", code)
	}

	res := stepUp(t, srv, idp, subject, time.Now())
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/settings/profiles" {
		t.Fatalf("finish: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if code, body := put(t, srv, "PUT", "/api/v1/profiles/"+owner+"/pin", `{"pin":"5678"}`); code != 204 {
		t.Errorf("change PIN after step-up: %d %s", code, body)
	}
	if code, body := put(t, srv, "DELETE", "/api/v1/profiles/"+owner+"/pin", ""); code != 204 {
		t.Errorf("remove PIN after step-up: %d %s", code, body)
	}

	_, body = post(t, srv, "/api/v1/profiles", `{"name":"Gone"}`)
	var gone struct{ ID string }
	json.Unmarshal([]byte(body), &gone)
	if code, body := put(t, srv, "DELETE", "/api/v1/profiles/"+gone.ID, ""); code != 204 {
		t.Errorf("delete profile: %d %s", code, body)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/profiles/"+gone.ID); code != 404 {
		t.Errorf("deleted profile still there: %d", code)
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/profiles/"+owner, ""); code != 400 {
		t.Errorf("deleted the owner profile: %d", code)
	}
}

func TestStepUpStateIsBoundToTheSession(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})
	res := browse(t, srv, "/api/v1/step-up?return=https://evil.example/", nil)
	res.Body.Close()
	code, state := idp.login(res.Header.Get("Location"), subject, time.Now())

	// The same person in another browser can't finish it.
	other := &http.Client{Transport: srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/auth/finish?code="+code+"&state="+state, nil)
	req.Header.Set("Remote-User", subject)
	res, _ = other.Do(req)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("finished from another session: %d", res.StatusCode)
	}
	// And the state was used up by that attempt.
	res = browse(t, srv, "/api/v1/auth/finish?code="+code+"&state="+state, nil)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("state reused: %d", res.StatusCode)
	}

	// An unsafe return address falls back to "/".
	res = stepUp(t, srv, idp, subject, time.Now())
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || loc != "/settings/profiles" {
		t.Errorf("finish: %d %s", res.StatusCode, loc)
	}
	res = browse(t, srv, "/api/v1/step-up?return=//evil.example/x", nil)
	res.Body.Close()
	code, state = idp.login(res.Header.Get("Location"), subject, time.Now())
	res = browse(t, srv, "/api/v1/auth/finish?code="+code+"&state="+state, nil)
	if loc := res.Header.Get("Location"); loc != "/" {
		t.Errorf("unsafe return led to %q", loc)
	}
}

// Only the callback accepts cross-site navigations.
func TestCrossSiteOnlyForTheCallback(t *testing.T) {
	idp := newFakeIdP(t)
	srv, _ := newServerWith(t, options{provider: idp.provider()})
	nav := map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"}
	for path, want := range map[string]int{
		"/api/v1/auth/callback?state=x": 200,
		"/api/v1/auth/finish?state=x":   403,
		"/api/v1/step-up":               403,
		"/api/v1/me":                    403,
	} {
		res := browse(t, srv, path, nav)
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, res.StatusCode, want)
		}
	}
	// A fetch, not a navigation, is refused even for the callback.
	res := browse(t, srv, "/api/v1/auth/callback?state=x", map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"})
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Errorf("cross-site fetch of the callback: %d", res.StatusCode)
	}
}
