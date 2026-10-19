package plugin

import (
	"net/http"
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
	r := NewRegistrar(http.NewServeMux())
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
