package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// Anyone may ask whose brand to show; the operator's files come as they
// were uploaded, with the API's headers.
func TestBranding(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	get := func(path string) (*http.Response, string) {
		res, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	if res, body := get("/api/v1/branding"); res.StatusCode != 200 || body != `{"custom":false,"name":"Overfin","banner":{}}`+"\n" {
		t.Errorf("Overfin's: %d %s", res.StatusCode, body)
	}
	ctx := t.Context()
	st.SetBrandingFile(ctx, "banner-dark", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), time.Unix(1_800_000_000, 0))
	// Files uploaded before the switch is on stay unused.
	if _, body := get("/api/v1/branding"); strings.Contains(body, "banner-dark") {
		t.Errorf("off, with a file: %s", body)
	}
	st.SetBranding(ctx, true, "zecopter")
	if _, body := get("/api/v1/branding"); body != `{"custom":true,"name":"zecopter","banner":{"dark":"/api/v1/branding/banner-dark?v=1800000000"}}`+"\n" {
		t.Errorf("on: %s", body)
	}
	res, body := get("/api/v1/branding/banner-dark?v=1800000000")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(body, "<svg") ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Errorf("file: %d %v", res.StatusCode, res.Header)
	}
	for _, path := range []string{"/api/v1/branding/logo", "/api/v1/branding/other"} {
		if res, _ := get(path); res.StatusCode != 404 {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
}
