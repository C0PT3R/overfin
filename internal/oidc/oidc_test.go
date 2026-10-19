package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// FakeIdP is an identity provider for tests. Each code maps to the claims
// its ID token will carry.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	codes map[string]map[string]any
	alg   string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{key: key, codes: map[string]map[string]any{}, alg: "RS256"}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) issuer() string { return f.srv.URL + "/o/app/" }

func (f *fakeIdP) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"):
		json.NewEncoder(w).Encode(map[string]string{
			"issuer": f.issuer(), "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
		})
	case r.URL.Path == "/jwks":
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
		}}})
	case r.URL.Path == "/token":
		user, pass, _ := r.BasicAuth()
		claims, ok := f.codes[r.FormValue("code")]
		if user != "overfin" || pass != "secret" || !ok || r.FormValue("code_verifier") != "verifier" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(claims)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeIdP) sign(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signed := enc(map[string]string{"alg": f.alg, "kid": "k1"}) + "." + enc(claims)
	if f.alg == "none" {
		return signed + "."
	}
	sum := sha256.Sum256([]byte(signed))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIdP) claims(now time.Time) map[string]any {
	return map[string]any{
		"iss": f.issuer(), "sub": "user-1", "aud": "overfin", "nonce": "nonce",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "auth_time": now.Unix(),
	}
}

func (f *fakeIdP) provider() *Provider {
	c := f.srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{Issuer: f.issuer(), ClientID: "overfin", ClientSecret: "secret",
		RedirectURL: "https://example.org/api/v1/step-up/callback", HTTP: c}
}

func TestAuthURL(t *testing.T) {
	f := newFakeIdP(t)
	u, err := f.provider().AuthURL(context.Background(), "state", "nonce", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"prompt=login", "code_challenge_method=S256", "code_challenge=" + Challenge("verifier"), "state=state", "nonce=nonce", "scope=openid"} {
		if !strings.Contains(u, want) {
			t.Errorf("auth URL lacks %q: %s", want, u)
		}
	}
}

func TestExchange(t *testing.T) {
	f := newFakeIdP(t)
	now := time.Now()
	ctx := context.Background()

	f.codes["good"] = f.claims(now)
	login, err := f.provider().Exchange(ctx, "good", "verifier", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	if login.Subject != "user-1" || login.AuthTime.Unix() != now.Unix() {
		t.Errorf("login = %+v", login)
	}

	bad := map[string]func(map[string]any){
		"wrong nonce":     func(c map[string]any) { c["nonce"] = "other" },
		"wrong audience":  func(c map[string]any) { c["aud"] = "someone-else" },
		"many audiences":  func(c map[string]any) { c["aud"] = []string{"overfin", "other"} },
		"wrong issuer":    func(c map[string]any) { c["iss"] = "https://evil.example/" },
		"expired":         func(c map[string]any) { c["exp"] = now.Add(-time.Hour).Unix() },
		"no login time":   func(c map[string]any) { delete(c, "auth_time") },
		"no subject":      func(c map[string]any) { c["sub"] = "" },
		"issued in later": func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() },
	}
	for name, change := range bad {
		c := f.claims(now)
		change(c)
		f.codes[name] = c
		if _, err := f.provider().Exchange(ctx, name, "verifier", "nonce"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// Unsigned and HMAC tokens are refused whatever they claim.
	for _, alg := range []string{"none", "HS256"} {
		f.alg = alg
		f.codes["alg"] = f.claims(now)
		if _, err := f.provider().Exchange(ctx, "alg", "verifier", "nonce"); err == nil {
			t.Errorf("alg %s accepted", alg)
		}
	}
	f.alg = "RS256"

	// A token signed by another key.
	p := f.provider()
	f.codes["good"] = f.claims(now)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	f.key, other = other, f.key
	p.keys = nil
	if _, err := p.Exchange(ctx, "good", "verifier", "nonce"); err != nil {
		t.Fatalf("after key rotation: %v", err)
	}
	p.keys = map[string]crypto.PublicKey{"k1": &other.PublicKey}
	p.keysFetch = time.Now()
	if _, err := p.Exchange(ctx, "good", "verifier", "nonce"); err == nil {
		t.Error("token signed by another key accepted")
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider()
	p.Issuer = f.srv.URL + "/o/other/"
	if _, err := p.AuthURL(context.Background(), "s", "n", "v"); err == nil {
		t.Error("accepted metadata for another issuer")
	}
}
