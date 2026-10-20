package site

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

var files = fstest.MapFS{
	"index.html":       {Data: []byte("home")},
	"app.js":           {Data: []byte("js")},
	".env":             {Data: []byte("secret")},
	"sub/index.html":   {Data: []byte("sub")},
	"sub/.git/config":  {Data: []byte("git")},
	"empty/readme.txt": {Data: []byte("no index here")},
}

func open(http.ResponseWriter, *http.Request) bool { return true }

func get(h http.Handler, method, path string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEmbedded(t *testing.T) {
	h, err := Embedded(files, "default-src 'self'", false, open)
	if err != nil {
		t.Fatal(err)
	}
	w := get(h, "GET", "/")
	tag := w.Header().Get("ETag")
	if w.Code != 200 || w.Body.String() != "home" || w.Header().Get("Content-Security-Policy") != "default-src 'self'" ||
		w.Header().Get("Cache-Control") != "no-cache" || tag == "" {
		t.Fatalf("root: %d %q %v", w.Code, w.Body, w.Header())
	}
	if w := get(h, "GET", "/", "If-None-Match", tag); w.Code != 304 {
		t.Errorf("revalidation: %d", w.Code)
	}
	if w := get(h, "GET", "/sub/"); w.Code != 200 || w.Body.String() != "sub" {
		t.Errorf("a folder's index: %d %q", w.Code, w.Body)
	}
	for _, p := range []string{"/.env", "/sub/.git/config", "/empty/", "/empty", "/../index.html/x", "/missing"} {
		if w := get(h, "GET", p); w.Code != 404 || strings.Contains(w.Body.String(), "readme") {
			t.Errorf("%s: %d %q", p, w.Code, w.Body)
		}
	}
	if w := get(h, "POST", "/"); w.Code != 405 {
		t.Errorf("POST: %d", w.Code)
	}
}

func TestGate(t *testing.T) {
	refuse := func(w http.ResponseWriter, _ *http.Request) bool {
		http.Error(w, "log in first", http.StatusUnauthorized)
		return false
	}
	h, _ := Embedded(files, "x", false, refuse)
	if w := get(h, "GET", "/app.js"); w.Code != 401 || strings.Contains(w.Body.String(), "js") {
		t.Errorf("gate: %d %q", w.Code, w.Body)
	}
}

func TestSinglePageApps(t *testing.T) {
	h, _ := Embedded(files, "x", true, open)
	if w := get(h, "GET", "/library/movies"); w.Code != 200 || w.Body.String() != "home" {
		t.Errorf("app route: %d %q", w.Code, w.Body)
	}
	if w := get(h, "GET", "/missing.js"); w.Code != 404 {
		t.Errorf("a missing file is still missing: %d", w.Code)
	}
	if w := get(h, "GET", "/.env"); w.Code != 404 {
		t.Errorf("hidden files stay hidden: %d", w.Code)
	}
}

// A folder's files are served live, and nothing outside it is reachable,
// not even through a symbolic link.
func TestFolder(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("outside"), 0o644)
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("v1"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	h, err := Folder(dir, "x", false, open)
	if err != nil {
		t.Fatal(err)
	}
	if w := get(h, "GET", "/"); w.Body.String() != "v1" {
		t.Errorf("root: %q", w.Body)
	}
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("v2"), 0o644)
	if w := get(h, "GET", "/"); w.Body.String() != "v2" {
		t.Errorf("after an update: %q", w.Body)
	}
	if w := get(h, "GET", "/link.txt"); w.Code != 404 || strings.Contains(w.Body.String(), "outside") {
		t.Errorf("symlink out of the folder: %d %q", w.Code, w.Body)
	}
}
