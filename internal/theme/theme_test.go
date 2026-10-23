package theme

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Light, chosen or followed, sets the same tokens as dark does.
func TestTokens(t *testing.T) {
	css := string(CSS)
	blocks := strings.Split(css, ":root")
	token := regexp.MustCompile(`(--[a-z0-9-]+):`)
	names := func(block string) map[string]bool {
		out := map[string]bool{}
		for _, m := range token.FindAllStringSubmatch(block, -1) {
			out[m[1]] = true
		}
		return out
	}
	dark, light, followed := names(blocks[1]), names(blocks[2]), names(blocks[3])
	for name := range light {
		if !dark[name] {
			t.Errorf("%s: light only", name)
		}
	}
	if len(light) != len(followed) {
		t.Errorf("light chosen sets %d tokens, followed %d", len(light), len(followed))
	}
	for name := range light {
		if !followed[name] {
			t.Errorf("%s: not set when light is followed", name)
		}
	}
}

func TestHandler(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/theme.css", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/css; charset=utf-8" || !strings.Contains(rec.Body.String(), "--bg:") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	req := httptest.NewRequest("GET", "/api/v1/theme.css", nil)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	rec = httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != 304 {
		t.Errorf("revalidated: %d", rec.Code)
	}
}

func TestColors(t *testing.T) {
	dark, light := Colors(false), Colors(true)
	if dark["bg"] != "#0c0d11" || light["bg"] != "#f4f5f8" || light["text"] != "#1b1e25" || dark["accent"] != "#6aa8ff" {
		t.Errorf("dark %v, light %v", dark, light)
	}
}
