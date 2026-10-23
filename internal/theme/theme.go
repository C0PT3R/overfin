// Package theme is Overfin's look: one stylesheet of values (colors,
// shadows, corners, font) that the site, the sign-in page, the control
// panel and the setup wizard all load, so changing one changes them all.
package theme

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
)

//go:embed theme.css
var CSS []byte

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
