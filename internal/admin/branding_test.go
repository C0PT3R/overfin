package admin

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBranding(t *testing.T) {
	p, b, _, _ := signedInPanel(t)
	if rec := b.do("GET", "/admin/api/branding", "", nil); rec.Code != 200 || rec.Body.String() != `{"on":false,"name":"","files":{}}`+"\n" {
		t.Errorf("before: %d %s", rec.Code, rec.Body)
	}
	for body, want := range map[string]string{
		`{"on":true,"name":""}`:                                "Give your site's name",
		`{"on":true,"name":"<b>Riverside</b>"}`:                "plain text",
		`{"on":true,"name":"` + strings.Repeat("z", 41) + `"}`: "40 characters",
	} {
		if rec := b.do("PUT", "/admin/api/branding", body, nil); rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := b.do("PUT", "/admin/api/branding", `{"on":true,"name":" Riverside "}`, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"on":true,"name":"Riverside"`) {
		t.Errorf("on: %d %s", rec.Code, rec.Body)
	}

	// A file goes as it is, with its image type; nothing else is taken.
	upload := func(path, contentType, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("Content-Type", contentType)
		for k, v := range b.cookies {
			req.Header.Add("Cookie", k+"="+v)
		}
		rec := httptest.NewRecorder()
		p.Handler().ServeHTTP(rec, req)
		return rec
	}
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`
	if rec := upload(brandingFiles+"banner-dark", "text/html", svg); rec.Code != 415 {
		t.Errorf("not an image: %d", rec.Code)
	}
	if rec := upload(brandingFiles+"other", "image/svg+xml", svg); rec.Code != 404 {
		t.Errorf("unknown kind: %d", rec.Code)
	}
	// No SVG, whatever type it comes as: emails can't show it.
	for _, ct := range []string{"image/svg+xml", "image/png"} {
		if rec := upload(brandingFiles+"banner-dark", ct, svg); rec.Code != 400 || !strings.Contains(rec.Body.String(), "SVG") {
			t.Errorf("SVG as %s: %d %s", ct, rec.Code, rec.Body)
		}
	}
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 64)
	if rec := upload(brandingFiles+"banner-dark", "image/png", png); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"banner-dark":"/admin/api/branding/files/banner-dark?v=`) {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	rec := b.do("GET", brandingFiles+"banner-dark", "", nil)
	if rec.Code != 200 || rec.Body.String() != png || rec.Header().Get("Content-Type") != "image/png" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("shown: %d %v", rec.Code, rec.Header())
	}
	if rec := upload(brandingFiles+"logo", "image/png", strings.Repeat("x", maxBrandingFile+1)); rec.Code != 413 {
		t.Errorf("too big: %d", rec.Code)
	}
	if rec := b.do("DELETE", brandingFiles+"banner-dark", "", nil); rec.Code != 200 || strings.Contains(rec.Body.String(), "banner-dark") {
		t.Errorf("removed: %d %s", rec.Code, rec.Body)
	}
}
