// Package oidc is a minimal OpenID Connect client: logging users in, and
// confirming a fresh login (step-up). It uses the authorization code flow
// with PKCE, and accepts a result only after checking the ID token's
// signature, issuer, audience, expiry, nonce and login time itself. Logins
// are re-checked with refresh tokens.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Provider is one identity provider and Overfin's client registration there.
type Provider struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// GroupsClaim names the ID token claim listing the user's groups; empty
	// when groups aren't needed.
	GroupsClaim string
	// HTTP makes the calls to the provider. Redirects are never followed.
	HTTP *http.Client

	mu        sync.Mutex
	meta      *metadata
	keys      map[string]crypto.PublicKey
	keysFetch time.Time
	now       func() time.Time
}

// NewClient returns an HTTP client for talking to a provider. When dial is
// set ("host:port"), connections go there instead of to the address DNS
// gives, while TLS is still verified against the issuer's own name: useful
// when the provider sits behind the same reverse proxy as Overfin.
func NewClient(dial string) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	if dial != "" {
		d := &net.Dialer{Timeout: 10 * time.Second}
		t.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, network, dial)
		}
	}
	return &http.Client{
		Transport: t,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

func (p *Provider) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// maxResponse caps every response from the provider.
const maxResponse = 1 << 20

func (p *Provider) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return p.doJSON(req, out)
}

func (p *Provider) doJSON(req *http.Request, out any) error {
	req.Header.Set("Accept", "application/json")
	res, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&e)
		if e.Error == "invalid_grant" {
			return ErrRevoked
		}
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: %s answered %d", req.URL.Path, res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, maxResponse)).Decode(out)
}

// discover reads the provider's metadata once; until it works, every step-up
// tries again, so Overfin starts even when the provider is down.
func (p *Provider) discover(ctx context.Context) (*metadata, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta != nil {
		return p.meta, nil
	}
	var m metadata
	if err := p.getJSON(ctx, strings.TrimSuffix(p.Issuer, "/")+"/.well-known/openid-configuration", &m); err != nil {
		return nil, err
	}
	if m.Issuer != p.Issuer {
		return nil, fmt.Errorf("oidc: provider says its issuer is %q, configured %q", m.Issuer, p.Issuer)
	}
	for _, e := range []string{m.AuthorizationEndpoint, m.TokenEndpoint, m.JWKSURI} {
		if u, err := url.Parse(e); err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("oidc: endpoint %q is not an https URL", e)
		}
	}
	p.meta = &m
	return p.meta, nil
}

// ErrRevoked: the provider refused a code or refresh token outright, for
// example because the user logged out, was disabled, or the code was used.
var ErrRevoked = errors.New("oidc: grant refused")

// LogoutURL is the provider's end-of-session address, if it has one.
func (p *Provider) LogoutURL(ctx context.Context) string {
	m, err := p.discover(ctx)
	if err != nil {
		return ""
	}
	return m.EndSessionEndpoint
}

// Random returns n random bytes, base64url-encoded: states, nonces, PKCE
// verifiers.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the PKCE S256 challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthURL is where to send the browser to log in. With fresh, the provider
// is asked for a new login even if the user has a session there. scope
// lists the scopes beyond "openid".
func (p *Provider) AuthURL(ctx context.Context, state, nonce, verifier string, fresh bool, scope ...string) (string, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(m.AuthorizationEndpoint)
	q := u.Query()
	for k, v := range map[string]string{
		"response_type":         "code",
		"client_id":             p.ClientID,
		"redirect_uri":          p.RedirectURL,
		"scope":                 strings.Join(append([]string{"openid"}, scope...), " "),
		"state":                 state,
		"nonce":                 nonce,
		"code_challenge":        Challenge(verifier),
		"code_challenge_method": "S256",
	} {
		q.Set(k, v)
	}
	if fresh {
		// Both ask for a new login. Neither is trusted: the login time in
		// the ID token is checked instead. max_age is 1, not 0: some
		// providers treat 0 as "not set".
		q.Set("prompt", "login")
		q.Set("max_age", "1")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Login is what a verified ID token says, and the refresh token that came
// with it, if any.
type Login struct {
	Subject  string
	AuthTime time.Time
	// Groups is nil when the token carries no groups claim.
	Groups []string
	// Name is a display name (preferred_username, else name), when the
	// token has one.
	Name         string
	RefreshToken string
}

// Exchange trades an authorization code for an ID token and verifies it.
func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (*Login, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.RedirectURL},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(p.ClientID), url.QueryEscape(p.ClientSecret))
	var tok struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := p.doJSON(req, &tok); err != nil {
		return nil, err
	}
	login, err := p.verify(ctx, m, tok.IDToken, nonce)
	if err != nil {
		return nil, err
	}
	login.RefreshToken = tok.RefreshToken
	return login, nil
}

// Refresh re-checks a login with its refresh token. The provider refuses
// (ErrRevoked) once the user logged out, was disabled or lost access. The
// returned login carries the refresh token to use next time, and the
// current groups when the provider sent a new ID token.
func (p *Provider) Refresh(ctx context.Context, refreshToken, subject string) (*Login, error) {
	m, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(p.ClientID), url.QueryEscape(p.ClientSecret))
	var tok struct {
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := p.doJSON(req, &tok); err != nil {
		return nil, err
	}
	login := &Login{Subject: subject, RefreshToken: tok.RefreshToken}
	if tok.RefreshToken == "" {
		login.RefreshToken = refreshToken // not rotated
	}
	if tok.IDToken != "" {
		got, err := p.verify(ctx, m, tok.IDToken, "")
		if err != nil {
			return nil, err
		}
		if got.Subject != subject {
			return nil, errors.New("oidc: refreshed token is for another user")
		}
		login.Groups, login.AuthTime = got.Groups, got.AuthTime
	}
	return login, nil
}

// skew is the clock difference tolerated between Overfin and the provider.
const skew = time.Minute

// verify checks an ID token. An empty nonce means a refreshed token, which
// carries neither nonce nor, necessarily, a login time.
func (p *Provider) verify(ctx context.Context, m *metadata, token, nonce string) (*Login, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: malformed ID token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &header); err != nil {
		return nil, err
	}
	key, err := p.key(ctx, m, header.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("oidc: malformed signature")
	}
	if err := checkSignature(header.Alg, key, parts[0]+"."+parts[1], sig); err != nil {
		return nil, err
	}

	var c struct {
		Iss      string          `json:"iss"`
		Sub      string          `json:"sub"`
		Aud      json.RawMessage `json:"aud"`
		Azp      string          `json:"azp"`
		Exp      *int64          `json:"exp"`
		Iat      *int64          `json:"iat"`
		AuthTime *int64          `json:"auth_time"`
		Nonce    string          `json:"nonce"`
		Username string          `json:"preferred_username"`
		FullName string          `json:"name"`
	}
	if err := decodePart(parts[1], &c); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := decodePart(parts[1], &all); err != nil {
		return nil, err
	}
	var aud []string
	if err := json.Unmarshal(c.Aud, &aud); err != nil {
		var one string
		if json.Unmarshal(c.Aud, &one) != nil {
			return nil, errors.New("oidc: bad audience")
		}
		aud = []string{one}
	}
	now := p.clock()
	switch {
	case c.Iss != m.Issuer:
		return nil, fmt.Errorf("oidc: token issued by %q, expected %q", c.Iss, m.Issuer)
	case !slices.Contains(aud, p.ClientID), len(aud) > 1 && c.Azp != p.ClientID:
		return nil, errors.New("oidc: token is for another client")
	case c.Exp == nil || now.After(time.Unix(*c.Exp, 0).Add(skew)):
		return nil, errors.New("oidc: token expired")
	case c.Iat == nil || time.Unix(*c.Iat, 0).After(now.Add(skew)):
		return nil, errors.New("oidc: token issued in the future")
	case nonce != "" && c.Nonce != nonce:
		return nil, errors.New("oidc: wrong nonce")
	case nonce != "" && c.AuthTime == nil:
		return nil, errors.New("oidc: no login time in token")
	case c.Sub == "":
		return nil, errors.New("oidc: no subject")
	}
	login := &Login{Subject: c.Sub, Name: c.Username}
	if login.Name == "" {
		login.Name = c.FullName
	}
	if c.AuthTime != nil {
		login.AuthTime = time.Unix(*c.AuthTime, 0)
	}
	if raw, ok := all[p.GroupsClaim]; ok && p.GroupsClaim != "" {
		login.Groups = []string{}
		if json.Unmarshal(raw, &login.Groups) != nil {
			return nil, errors.New("oidc: groups claim is not a list of names")
		}
	}
	return login, nil
}

func decodePart(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return errors.New("oidc: malformed token part")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errors.New("oidc: malformed token part")
	}
	return nil
}

// checkSignature accepts RS256 and ES256 only; "none" and HMAC algorithms
// are never accepted.
func checkSignature(alg string, key crypto.PublicKey, signed string, sig []byte) error {
	sum := sha256.Sum256([]byte(signed))
	switch k := key.(type) {
	case *rsa.PublicKey:
		if alg != "RS256" {
			break
		}
		if rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig) != nil {
			return errors.New("oidc: bad signature")
		}
		return nil
	case *ecdsa.PublicKey:
		if alg != "ES256" || len(sig) != 64 {
			break
		}
		r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(k, sum[:], r, s) {
			return errors.New("oidc: bad signature")
		}
		return nil
	}
	return fmt.Errorf("oidc: algorithm %q not accepted for this key", alg)
}

// key returns the signing key with this ID, fetching the provider's keys
// again when it's unknown (keys rotate), at most once a minute.
func (p *Provider) key(ctx context.Context, m *metadata, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if !p.keysFetch.IsZero() && p.clock().Sub(p.keysFetch) < time.Minute {
		return nil, errors.New("oidc: unknown signing key")
	}
	p.keysFetch = p.clock()
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Use string `json:"use"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, m.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) > 4 || len(n) < 256 {
				continue // malformed, or under 2048 bits
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if k.Crv != "P-256" || err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
			if err != nil {
				continue // not on the curve
			}
			keys[k.Kid] = pub
		}
	}
	p.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errors.New("oidc: unknown signing key")
}
