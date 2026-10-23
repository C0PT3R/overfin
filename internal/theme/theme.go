// Package theme is Overfin's look: one stylesheet of values (colors,
// shadows, corners, font) that the site, the sign-in page, the control
// panel and the setup wizard all load, so changing one changes them all.
package theme

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
)

//go:embed theme.css
var CSS []byte

// Banner is Overfin's banner, its logo and its name as one picture, to put
// in a page as it is: the name takes the page's text color, so it suits
// dark and light pages.
//
//go:embed banner.svg
var Banner string

var etag = func() string {
	sum := sha256.Sum256(CSS)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}()

// Handler serves the stylesheet, revalidated on every use.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(CSS)
	})
}

// Colors are the theme's values, dark or light, by name ("bg", "text",
// "accent"…), for what can't load the stylesheet: emails, written inline.
func Colors(light bool) map[string]string {
	css := string(CSS)
	block := css[strings.Index(css, ":root {"):]
	if light {
		block = css[strings.Index(css, `:root[data-theme="light"] {`):]
	}
	block = block[:strings.Index(block, "}")]
	out := map[string]string{}
	for _, m := range token.FindAllStringSubmatch(block, -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

var token = regexp.MustCompile(`--([a-z0-9-]+):\s*([^;]+);`)
