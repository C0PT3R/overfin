package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/C0PT3R/overfin/internal/store"
	"github.com/C0PT3R/overfin/internal/textclean"
)

// Account templates: what accounts may do. Accounts follow theirs live, so
// editing a template changes every account on it at once. Default exists
// from the start, so accounts can be opened right away; it's edited like any
// other, and only can't be deleted.

type templateJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MaxStreams  int    `json:"maxStreams"`
	MaxProfiles int    `json:"maxProfiles"`
	Accounts    int    `json:"accounts"`
	Default     bool   `json:"default"`
}

func toTemplateJSON(t *store.Template) templateJSON {
	return templateJSON{ID: t.ID, Name: t.Name, MaxStreams: t.MaxStreams, MaxProfiles: t.MaxProfiles,
		Accounts: t.Accounts, Default: t.ID == store.DefaultTemplate}
}

func (p *Panel) templates(w http.ResponseWriter, r *http.Request, _ *session) {
	all, err := p.Store.Templates(r.Context())
	if err != nil {
		p.fail(w, "listing templates", err)
		return
	}
	out := []templateJSON{}
	for _, t := range all {
		out = append(out, toTemplateJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type templateInput struct {
	Name        string `json:"name"`
	MaxStreams  int    `json:"maxStreams"`
	MaxProfiles int    `json:"maxProfiles"`
}

// readTemplate decodes and checks a template; it writes the problem and
// returns false when it's not acceptable.
func readTemplate(w http.ResponseWriter, r *http.Request) (templateInput, bool) {
	var in templateInput
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	switch {
	case dec.Decode(&in) != nil:
		invalid(w, "not a valid template")
	case in.Name == "" || len(in.Name) > 40 || textclean.Plain(in.Name, 40) != in.Name:
		invalid(w, "a template's name is plain text, 1 to 40 characters")
	case in.MaxStreams < 1 || in.MaxStreams > 100:
		invalid(w, "streams at once: 1 to 100")
	case in.MaxProfiles < 1 || in.MaxProfiles > 20:
		invalid(w, "profiles: 1 to 20, the owner's included")
	default:
		return in, true
	}
	return in, false
}

func (p *Panel) createTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	in, ok := readTemplate(w, r)
	if !ok {
		return
	}
	t, err := p.Store.CreateTemplate(r.Context(), in.Name, in.MaxStreams, in.MaxProfiles)
	if errors.Is(err, store.ErrTemplateNameTaken) {
		invalid(w, "another template has that name")
		return
	} else if err != nil {
		p.fail(w, "creating a template", err)
		return
	}
	writeJSON(w, http.StatusCreated, toTemplateJSON(t))
}

func (p *Panel) updateTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	in, ok := readTemplate(w, r)
	if !ok {
		return
	}
	switch err := p.Store.UpdateTemplate(r.Context(), r.PathValue("id"), in.Name, in.MaxStreams, in.MaxProfiles); {
	case errors.Is(err, store.ErrTemplateNameTaken):
		invalid(w, "another template has that name")
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "No such template", http.StatusNotFound)
	case err != nil:
		p.fail(w, "changing a template", err)
	default:
		p.Logger.Info("account template changed in the control panel", "template", r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	}
}

func (p *Panel) deleteTemplate(w http.ResponseWriter, r *http.Request, _ *session) {
	switch err := p.Store.DeleteTemplate(r.Context(), r.PathValue("id")); {
	case errors.Is(err, store.ErrTemplateInUse):
		invalid(w, "Default can't be deleted, nor a template accounts still follow: move them first")
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "No such template", http.StatusNotFound)
	case err != nil:
		p.fail(w, "deleting a template", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
