package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/overfin/server/internal/store"
)

// People are those Overfin signs in itself (managed sign-in), by email:
// who may sign in, and who's an admin. Their accounts are in Users.
type People interface {
	// SigninLink makes a one-time sign-in link for an email, to hand
	// over; its path on the site.
	SigninLink(ctx context.Context, email string) (string, error)
}

type personJSON struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
	HasPassword bool   `json:"hasPassword"`
	// AccountID is their account, once they signed in and made one.
	AccountID string `json:"accountId,omitempty"`
	// You: the admin asking.
	You       bool   `json:"you,omitempty"`
	CreatedAt string `json:"createdAt"`
}

func (p *Panel) people(w http.ResponseWriter, r *http.Request, s *session) {
	all, err := p.Store.Users(r.Context())
	if err != nil {
		p.fail(w, "listing people", err)
		return
	}
	out := []personJSON{}
	for _, u := range all {
		pj := personJSON{ID: u.Subject, Email: u.Email, Admin: u.Admin, Disabled: u.Disabled,
			HasPassword: u.PasswordHash != "", CreatedAt: u.Created.Format("2006-01-02T15:04:05Z07:00"),
			You: strings.EqualFold(u.Email, s.name)}
		if a, err := p.Store.AccountBySubject(r.Context(), u.Subject); err == nil {
			pj.AccountID = a.ID
		} else if !errors.Is(err, store.ErrNotFound) {
			p.fail(w, "listing people", err)
			return
		}
		out = append(out, pj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (p *Panel) addPerson(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Email string `json:"email"`
		Admin bool   `json:"admin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid person")
		return
	}
	email := strings.TrimSpace(req.Email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		invalid(w, "That isn't an email address.")
		return
	}
	u, err := p.Store.CreateUser(r.Context(), email, req.Admin)
	if errors.Is(err, store.ErrEmailTaken) {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "conflict", "detail": "Someone already has that email."})
		return
	}
	if err != nil {
		p.fail(w, "adding someone", err)
		return
	}
	p.Logger.Info("control panel: someone added", "admin", req.Admin)
	writeJSON(w, http.StatusOK, personJSON{ID: u.Subject, Email: u.Email, Admin: u.Admin, CreatedAt: u.Created.Format("2006-01-02T15:04:05Z07:00")})
}

// changePerson makes someone an admin or not. Admins can't take it from
// themselves: someone must stay able to come in.
func (p *Panel) changePerson(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Admin *bool `json:"admin"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Admin == nil {
		invalid(w, "not a valid change")
		return
	}
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if strings.EqualFold(u.Email, s.name) && !*req.Admin {
		invalid(w, "You can't stop being an admin yourself; another admin can.")
		return
	}
	if err := p.Store.SetUserAdmin(r.Context(), u.Subject, *req.Admin); err != nil {
		p.fail(w, "changing someone", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"admin": *req.Admin})
}

// personLink makes a one-time sign-in link to hand to someone: for a
// server without email, or someone who lost their password.
func (p *Panel) personLink(w http.ResponseWriter, r *http.Request, _ *session) {
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if u.Disabled {
		invalid(w, "This person is disabled.")
		return
	}
	link, err := p.People.SigninLink(r.Context(), u.Email)
	if err != nil {
		p.fail(w, "making a sign-in link", err)
		return
	}
	p.Logger.Info("control panel: a sign-in link was made")
	writeJSON(w, http.StatusOK, map[string]string{"link": link})
}

// removePerson removes someone who has no account yet; with one, deleting
// the account removes them.
func (p *Panel) removePerson(w http.ResponseWriter, r *http.Request, s *session) {
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	if strings.EqualFold(u.Email, s.name) {
		invalid(w, "You can't remove yourself.")
		return
	}
	if _, err := p.Store.AccountBySubject(r.Context(), u.Subject); err == nil {
		invalid(w, "They have an account: delete it in Users, which removes them too.")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		p.fail(w, "removing someone", err)
		return
	}
	if err := p.Store.DeleteUser(r.Context(), u.Subject); err != nil {
		p.fail(w, "removing someone", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) person(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, err := p.Store.UserBySubject(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such person", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		p.fail(w, "reading someone", err)
		return nil, false
	}
	return u, true
}
