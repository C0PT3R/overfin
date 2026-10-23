package admin

import (
	"net/http"
	"slices"

	"github.com/overfin/server/internal/lang"
)

// languages are the languages the operator offers, the default among them,
// and those that can be offered.
func (p *Panel) languages(w http.ResponseWriter, r *http.Request, s *session) {
	offered, def, _, err := p.Store.Languages(r.Context())
	if err != nil {
		p.fail(w, "reading the languages", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offered": offered, "default": def, "choices": lang.Choices})
}

// setLanguages changes the languages offered and the default. Changing
// the default's language has titles' texts fetched again in it.
func (p *Panel) setLanguages(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Offered []string `json:"offered"`
		Default string   `json:"default"`
	}
	if !decode(w, r, &req) {
		return
	}
	if detail := checkLanguages(req.Offered, req.Default); detail != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
		return
	}
	changed, err := p.Store.SetLanguages(r.Context(), req.Offered, req.Default)
	if err != nil {
		p.fail(w, "changing the languages", err)
		return
	}
	p.Logger.Info("the languages changed in the control panel", "offered", req.Offered, "default", req.Default, "by", s.name)
	if changed && p.OnLanguage != nil {
		p.OnLanguage()
	}
	p.languages(w, r, s)
}

// checkLanguages says what's wrong with a choice of languages, in plain
// words; "" when nothing is.
func checkLanguages(offered []string, def string) string {
	if len(offered) == 0 {
		return "Offer at least one language."
	}
	for i, tag := range offered {
		if !lang.Offered(tag) || slices.Contains(offered[:i], tag) {
			return "Those aren't languages Overfin offers, each once."
		}
	}
	if !slices.Contains(offered, def) {
		return "The default language must be one of those offered."
	}
	return ""
}
