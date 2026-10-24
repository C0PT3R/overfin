package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/overfin/server/internal/password"
)

// Registration, with Overfin's own sign-in: what a new password must have,
// and whether invitations can go by email (they work only then: the
// person's address is confirmed by a link). The invitations themselves are
// the invitations module's.

type registrationJSON struct {
	Rules password.Rules `json:"rules"`
	// Mail says whether emails with links can be sent.
	Mail bool `json:"mail"`
}

func (p *Panel) registrationState(r *http.Request) (registrationJSON, error) {
	rules, err := p.Store.PasswordRules(r.Context())
	return registrationJSON{Rules: rules, Mail: p.People.CanMail()}, err
}

func (p *Panel) registration(w http.ResponseWriter, r *http.Request, _ *session) {
	out, err := p.registrationState(r)
	if err != nil {
		p.fail(w, "reading the registration's settings", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// setRegistration changes the password rules: they apply to every new
// password from then on.
func (p *Panel) setRegistration(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Rules password.Rules `json:"rules"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not valid settings")
		return
	}
	if !req.Rules.Valid() {
		invalid(w, "A password has 10 to 64 characters at least.")
		return
	}
	if err := p.Store.SetPasswordRules(r.Context(), req.Rules); err != nil {
		p.fail(w, "saving the password rules", err)
		return
	}
	p.Logger.Info("password rules changed in the control panel", "min", req.Rules.MinLength,
		"digit", req.Rules.Digit, "special", req.Rules.Special, "by", s.name)
	p.registration(w, r, s)
}

// sendInvitation emails an invitation's link (made in the invitations
// tab) to someone.
func (p *Panel) sendInvitation(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Email string `json:"email"`
		Link  string `json:"link"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid request")
		return
	}
	email := strings.TrimSpace(req.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		invalid(w, "That isn't an email address.")
		return
	}
	if i := strings.Index(req.Link, "/invite/"); i < 0 || len(req.Link) > 300 || strings.ContainsAny(req.Link, " \r\n<>\"") {
		invalid(w, "That isn't an invitation's link.")
		return
	}
	if !p.People.CanMail() {
		invalid(w, "Email is off: set it up first, with the site's address.")
		return
	}
	if err := p.People.SendInvitation(r.Context(), email, req.Link); err != nil {
		var plain Problem
		if errors.As(err, &plain) {
			invalid(w, string(plain))
			return
		}
		invalid(w, err.Error())
		return
	}
	p.Logger.Info("an invitation was sent by email from the control panel")
	writeJSON(w, http.StatusOK, map[string]string{"summary": "Sent to " + email + "."})
}
