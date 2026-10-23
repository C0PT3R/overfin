package api

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Branding tells sites and the sign-in page whose brand to show: Overfin's,
// or the operator's own (its name, logo and banners) once switched on in
// the control panel. Anyone may ask, signed in or not.
type Branding struct {
	Store *store.Store
}

func (Branding) Name() string { return "branding" }

func (b Branding) Register(r *plugin.Registrar) error {
	if err := r.HandlePublic("GET /api/v1/branding", http.HandlerFunc(b.get)); err != nil {
		return err
	}
	return r.HandlePublic("GET /api/v1/branding/{kind}", http.HandlerFunc(b.file))
}

// brandingJSON is the brand to show. Without Custom, it's Overfin's: the
// clients have its logo and banner. Files the operator didn't upload are
// absent: a site then shows Overfin's logo, the name instead of a banner,
// and the dark banner on light pages too.
type brandingJSON struct {
	Custom bool   `json:"custom"`
	Name   string `json:"name"`
	Logo   string `json:"logo,omitempty"`
	Banner struct {
		Dark  string `json:"dark,omitempty"`
		Light string `json:"light,omitempty"`
	} `json:"banner"`
}

// BrandName is the name people see: the operator's when their branding is
// on, else Overfin's.
func BrandName(br store.Branding) string {
	if br.On && br.Name != "" {
		return br.Name
	}
	return "Overfin"
}

func (b Branding) get(w http.ResponseWriter, r *http.Request) {
	br, err := b.Store.Branding(r.Context())
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	out := brandingJSON{Custom: br.On, Name: BrandName(br)}
	if br.On {
		url := func(kind string) string {
			at, ok := br.Files[kind]
			if !ok {
				return ""
			}
			return fmt.Sprintf("/api/v1/branding/%s?v=%d", kind, at.Unix())
		}
		out.Logo, out.Banner.Dark, out.Banner.Light = url("logo"), url("banner-dark"), url("banner-light")
	}
	writeOK(w, out)
}

// file serves a branding file as the operator uploaded it, revalidated on
// every use. The API's own headers apply: no script in it ever runs.
func (b Branding) file(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if !slices.Contains(store.BrandingKinds, kind) {
		problem.Send(w, problem.NotFound, "")
		return
	}
	f, ok, err := b.Store.BrandingFileOf(r.Context(), kind)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	tag := `"` + strconv.FormatInt(f.Updated.Unix(), 36) + `"`
	h := w.Header()
	h.Set("Content-Type", f.ContentType)
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(f.Data)
}
