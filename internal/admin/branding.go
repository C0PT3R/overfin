package admin

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/overfin/server/internal/store"
)

// The operator's branding: a switch, their site's name, and their logo and
// banners, uploaded as they are. The control panel itself always stays
// Overfin's.

// brandingFiles is where branding files are uploaded, shown and removed:
// /admin/api/branding/files/{kind}.
const brandingFiles = "/admin/api/branding/files/"

// maxBrandingFile is the largest branding file taken.
const maxBrandingFile = 4 << 20

// maxBrandName is the longest name, in characters.
const maxBrandName = 40

type brandingState struct {
	On   bool   `json:"on"`
	Name string `json:"name"`
	// Files are the uploaded files' addresses here, by kind.
	Files map[string]string `json:"files"`
}

func (p *Panel) brandingState(r *http.Request) (brandingState, error) {
	br, err := p.Store.Branding(r.Context())
	if err != nil {
		return brandingState{}, err
	}
	out := brandingState{On: br.On, Name: br.Name, Files: map[string]string{}}
	for kind, at := range br.Files {
		out.Files[kind] = fmt.Sprintf("%s%s?v=%d", brandingFiles, kind, at.Unix())
	}
	return out, nil
}

func (p *Panel) branding(w http.ResponseWriter, r *http.Request, s *session) {
	out, err := p.brandingState(r)
	if err != nil {
		p.fail(w, "reading the branding", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// brandName checks a site's name: plain text, one line, not too long.
func brandName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxBrandName {
		return "", Problem(fmt.Sprintf("The name is %d characters at most.", maxBrandName))
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '<' || r == '>' }) {
		return "", Problem("The name is plain text, on one line.")
	}
	return name, nil
}

func (p *Panel) setBranding(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		On   bool   `json:"on"`
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, err := brandName(req.Name)
	if err == nil && req.On && name == "" {
		err = Problem("Give your site's name to use your own branding.")
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	if err := p.Store.SetBranding(r.Context(), req.On, name); err != nil {
		p.fail(w, "saving the branding", err)
		return
	}
	p.Logger.Info("branding changed in the control panel", "on", req.On, "by", s.name)
	p.branding(w, r, s)
}

// brandingKind is the file kind a request names, or "" after answering
// 404.
func brandingKind(w http.ResponseWriter, r *http.Request) string {
	kind := r.PathValue("kind")
	if !slices.Contains(store.BrandingKinds, kind) {
		http.NotFound(w, r)
		return ""
	}
	return kind
}

// uploadBrandingFile keeps a file as it's sent, with its image type.
func (p *Panel) uploadBrandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	data, err := io.ReadAll(r.Body)
	var tooBig *http.MaxBytesError
	switch {
	case errors.As(err, &tooBig):
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"type": "invalid-request",
			"detail": fmt.Sprintf("A file is %d MB at most.", maxBrandingFile>>20)})
		return
	case err != nil:
		return
	case len(data) == 0:
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "That file is empty."})
		return
	}
	if err := p.Store.SetBrandingFile(r.Context(), kind, mt, data, time.Now()); err != nil {
		p.fail(w, "saving a branding file", err)
		return
	}
	p.Logger.Info("a branding file was uploaded in the control panel", "kind", kind, "type", mt, "bytes", len(data), "by", s.name)
	p.branding(w, r, s)
}

// brandingFile shows a file here, for the page's previews.
func (p *Panel) brandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	f, ok, err := p.Store.BrandingFileOf(r.Context(), kind)
	if err != nil {
		p.fail(w, "reading a branding file", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("Cache-Control", "no-cache")
	// Shown as an image only: nothing in it ever runs.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	_, _ = w.Write(f.Data)
}

func (p *Panel) removeBrandingFile(w http.ResponseWriter, r *http.Request, s *session) {
	kind := brandingKind(w, r)
	if kind == "" {
		return
	}
	if err := p.Store.DeleteBrandingFile(r.Context(), kind); err != nil {
		p.fail(w, "removing a branding file", err)
		return
	}
	p.Logger.Info("a branding file was removed in the control panel", "kind", kind, "by", s.name)
	p.branding(w, r, s)
}
