package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	h, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
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
		res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: status = %d, want 404", path, res.StatusCode)
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
