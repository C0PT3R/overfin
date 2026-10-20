package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/C0PT3R/overfin/module"
)

type demoModule struct{}

func (demoModule) Info() module.Info       { return module.Info{Name: "demo"} }
func (demoModule) Setup(module.Host) error { return nil }

func TestModuleAdminRoutes(t *testing.T) {
	mux := http.NewServeMux()
	h := &host{mount: Mount{Module: demoModule{}, Admin: mux}}
	for _, bad := range []string{"GET /x/{rest...}", "GET /x/", "/x", "GET x", "TRACE /x"} {
		if err := h.HandleAdmin(bad, func(http.ResponseWriter, *http.Request) {}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := h.HandleAdmin("GET /stats", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(299) }); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/api/modules/demo/stats", nil))
	if rec.Code != 299 {
		t.Errorf("filed under the module's prefix: %d", rec.Code)
	}
	// Without a control panel, admin routes are simply not served.
	h = &host{mount: Mount{Module: demoModule{}}}
	if err := h.HandleAdmin("GET /stats", func(http.ResponseWriter, *http.Request) {}); err != nil {
		t.Errorf("no panel: %v", err)
	}
}
