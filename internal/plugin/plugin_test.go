package plugin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckPattern(t *testing.T) {
	tests := []struct {
		pattern string
		ok      bool
	}{
		{"GET /api/v1/items/{id}", true},
		{"POST /api/v1/playback/{session}/progress", true},
		{"/api/v1/items", false}, // no method
		{"PATCH /api/v1/profiles/{id}", true},
		{"HEAD /api/v1/items", false},      // method not allowed
		{"GET /items", false},              // outside /api/v1/
		{"GET /api/items", false},          // unversioned
		{"GET /api/v1/", false},            // whole subtree
		{"GET /api/v1/jellyfin/", false},   // whole subtree
		{"GET /api/v1/{rest...}", false},   // catch-all
		{"GET /api/v1/x/{path...}", false}, // catch-all
	}
	for _, tt := range tests {
		err := checkPattern(tt.pattern)
		if (err == nil) != tt.ok {
			t.Errorf("checkPattern(%q) error = %v, want ok = %v", tt.pattern, err, tt.ok)
		}
	}
}

type fakePlugin struct {
	name    string
	pattern string
}

func (f fakePlugin) Name() string { return f.name }

func (f fakePlugin) Register(r *Registrar) error {
	return r.HandleFunc(f.pattern, func(http.ResponseWriter, *http.Request) {})
}

func TestLoadRejectsDuplicateRoutes(t *testing.T) {
	r := NewRegistrar(http.NewServeMux(), func(h http.Handler) http.Handler { return h })
	err := r.Load(
		fakePlugin{"a", "GET /api/v1/things"},
		fakePlugin{"b", "GET /api/v1/things"},
	)
	if err == nil {
		t.Fatal("expected an error for a duplicate route")
	}
	if got := r.Routes()["GET /api/v1/things"]; got != "a" {
		t.Errorf("route owner = %q, want %q", got, "a")
	}
}

func TestHandleProtectsAndHandlePublicDoesNot(t *testing.T) {
	protected := map[string]bool{}
	mux := http.NewServeMux()
	r := NewRegistrar(mux, func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			protected[req.URL.Path] = true
			h.ServeHTTP(w, req)
		})
	})
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if err := r.Handle("GET /api/v1/private", noop); err != nil {
		t.Fatal(err)
	}
	if err := r.HandlePublic("GET /api/v1/open", noop); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/private", "/api/v1/open"} {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}
	if !protected["/api/v1/private"] {
		t.Error("Handle did not wrap the route with protection")
	}
	if protected["/api/v1/open"] {
		t.Error("HandlePublic wrapped the route with protection")
	}
	if r.IsPublic("GET /api/v1/private") || !r.IsPublic("GET /api/v1/open") {
		t.Error("IsPublic is wrong")
	}
}

// Navigation routes: a fixed path, or one ending in a code.
func TestNavigationCodes(t *testing.T) {
	r := NewRegistrar(http.NewServeMux(), func(h http.Handler) http.Handler { return h })
	ok := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	for _, p := range []string{"GET /api/v1/signin", "GET /api/v1/invite/{code}"} {
		if err := r.HandleNavigation(p, ok); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"GET /api/v1/{x}/invite", "GET /api/v1/a/{b}/{c}", "GET /api/v1/files/{rest...}", "POST /api/v1/x/{code}"} {
		if err := r.HandleNavigation(p, ok); err == nil {
			t.Errorf("%s was accepted", p)
		}
	}
	for path, want := range map[string]bool{"/api/v1/signin": true, "/api/v1/invite/abc": true, "/api/v1/invite/": false,
		"/api/v1/invite/a/b": false, "/api/v1/signin/x": false} {
		if r.IsNavigation(path) != want {
			t.Errorf("IsNavigation(%s) = %v", path, !want)
		}
	}
}
