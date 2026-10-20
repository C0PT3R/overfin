// Package site serves the website at the root, beside the API: overfin-web
// by default, or the operator's own client from a folder. Only visitors the
// gate lets through get its files. Files are served as they are: never a
// directory listing, never a name starting with a dot, never anything
// outside the folder.
package site

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// Gate lets a request through, or answers it itself and reports false.
type Gate func(w http.ResponseWriter, r *http.Request) bool

// Handler serves the site's files.
type Handler struct {
	files fs.FS
	csp   string
	spa   bool
	gate  Gate
	// etags are the embedded files' content hashes: they have no dates, so
	// browsers revalidate them by hash.
	etags map[string]string
}

// Embedded serves files compiled into Overfin, such as overfin-web.
func Embedded(files fs.FS, csp string, spa bool, gate Gate) (*Handler, error) {
	h := &Handler{files: files, csp: csp, spa: spa, gate: gate, etags: map[string]string{}}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		h.etags[name] = `"` + hex.EncodeToString(sum[:12]) + `"`
		return nil
	})
	return h, err
}

// Folder serves the files in dir, read as they are on every request, so a
// new version of the site needs no restart. Nothing outside dir can be
// reached, not even through a symbolic link.
func Folder(dir, csp string, spa bool, gate Gate) (*Handler, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Handler{files: root.FS(), csp: csp, spa: spa, gate: gate}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.gate(w, r) {
		return
	}
	name, ok := fileName(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, info, err := h.open(name)
	// A path that isn't a file, in a single-page app, is one of its own
	// pages: index.html routes it.
	if errors.Is(err, fs.ErrNotExist) && h.spa && !strings.Contains(path.Base(name), ".") {
		name = "index.html"
		f, info, err = h.open(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hd := w.Header()
	hd.Set("Content-Security-Policy", h.csp)
	hd.Set("Cache-Control", "no-cache") // revalidated on every use
	modified := info.ModTime()
	if tag, ok := h.etags[name]; ok {
		hd.Set("ETag", tag)
		modified = time.Time{}
	}
	http.ServeContent(w, r, name, modified, rs)
}

// open opens a file, or a folder's index.html; folders are never listed.
func (h *Handler) open(name string) (fs.File, fs.FileInfo, error) {
	f, err := h.files.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.IsDir() {
		return f, info, nil
	}
	f.Close()
	if name == "index.html" || strings.HasSuffix(name, "/index.html") {
		return nil, nil, fs.ErrNotExist
	}
	return h.open(path.Join(name, "index.html"))
}

// fileName turns a request path into a file name in the site: the root and
// folders are their index.html, and names starting with a dot (.git, .env)
// are refused.
func fileName(p string) (string, bool) {
	name := strings.TrimPrefix(path.Clean("/"+p), "/")
	if name == "" {
		return "index.html", true
	}
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", false
		}
	}
	return name, fs.ValidPath(name)
}
