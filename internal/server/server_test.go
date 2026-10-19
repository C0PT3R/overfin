package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/plugin"
)

// The test server's client connects from 127.0.0.1, which these tests treat
// as the trusted proxy.
var loopback = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}

type whoami struct{}

func (whoami) Name() string { return "test" }

func (whoami) Register(r *plugin.Registrar) error {
	return r.HandleFunc("GET /api/v1/whoami", func(w http.ResponseWriter, req *http.Request) {
		id, _ := identity.FromContext(req.Context())
		_, _ = w.Write([]byte(id.Subject))
	})
}

func newTestServer(t *testing.T) *httptest.Server {
	return newServerWith(t, identity.NewForwardAuth(loopback, "Remote-User", ""))
}

func newServerWith(t *testing.T, ids *identity.Resolver) *httptest.Server {
	t.Helper()
	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)), ids, whoami{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// Paths that look like Jellyfin or Seerr routes must not reach anything.
func TestUnknownPathsAre404(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{
		"/",
		"/api",
		"/api/System/Info",
		"/api/Users",
		"/api/v1/settings/main",
		"/web/index.html",
		"/api/health",
		"/api/v1/health/../../System/Info",
	} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Type string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, res.StatusCode)
		}
		if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s: Content-Type = %q, want application/problem+json", path, ct)
		}
		if body.Type != "not-found" {
			t.Errorf("GET %s: type = %q, want not-found", path, body.Type)
		}
	}
}

// A known path with the wrong method is treated like any unknown route.
func TestWrongMethodIs404(t *testing.T) {
	srv := newTestServer(t)
	res, err := http.Post(srv.URL+"/api/v1/health", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
}

func TestProtectedRouteNeedsIdentity(t *testing.T) {
	srv := newTestServer(t)

	res, err := http.Get(srv.URL + "/api/v1/whoami")
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Type string }
	_ = json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || body.Type != "not-authenticated" {
		t.Errorf("without identity: status %d, type %q; want 401 not-authenticated", res.StatusCode, body.Type)
	}

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/whoami", nil)
	req.Header.Set("Remote-User", "abc123")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || string(got) != "abc123" {
		t.Errorf("with identity: status %d, body %q; want 200 abc123", res.StatusCode, got)
	}
}

// When the peer isn't a trusted proxy, identity headers mean nothing.
func TestUntrustedPeerCannotClaimIdentity(t *testing.T) {
	other := []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}
	srv := newServerWith(t, identity.NewForwardAuth(other, "Remote-User", ""))

	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/whoami", nil)
	req.Header.Set("Remote-User", "admin")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestHealthIsPublic(t *testing.T) {
	other := []netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")}
	srv := newServerWith(t, identity.NewForwardAuth(other, "Remote-User", ""))
	res, err := http.Get(srv.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}
