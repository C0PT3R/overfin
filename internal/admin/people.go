package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"

	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/store"
)

// People are those Overfin signs in itself (managed sign-in), by email
// and password: who may sign in, and who's an admin. Their accounts are in
// Users.
type People interface {
	// CanMail says whether emails with links can be sent (invitations by
	// email, forgotten passwords).
	CanMail() bool
	// SendInvitation emails an invitation's link.
	SendInvitation(ctx context.Context, to, link string) error
}

type personJSON struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
	HasPassword bool   `json:"hasPassword"`
	// Confirmed is false until someone who registered confirms their
	// address; MustChange, that they choose a new password at their next
	// sign-in.
	Confirmed  bool `json:"confirmed"`
	MustChange bool `json:"mustChange"`
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
			HasPassword: u.PasswordHash != "", Confirmed: u.Confirmed, MustChange: u.MustChange,
			CreatedAt: u.Created.Format("2006-01-02T15:04:05Z07:00"),
			You:       strings.EqualFold(u.Email, s.name)}
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

// addPerson makes someone's account here: their email and a first
// password, which they must change at their first sign-in unless the
// admin says otherwise.
func (p *Panel) addPerson(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		MustChange bool   `json:"mustChange"`
		Admin      bool   `json:"admin"`
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
	hash, ok := p.newPassword(w, r, req.Password)
	if !ok {
		return
	}
	ctx := r.Context()
	u, err := p.Store.CreateUser(ctx, email, req.Admin)
	if errors.Is(err, store.ErrEmailTaken) {
		writeJSON(w, http.StatusConflict, map[string]string{"type": "conflict", "detail": "Someone already has that email."})
		return
	}
	if err == nil {
		err = p.Store.SetPassword(ctx, u.Subject, hash)
	}
	if err == nil {
		err = p.Store.SetMustChange(ctx, u.Subject, req.MustChange)
	}
	if err != nil {
		p.fail(w, "adding someone", err)
		return
	}
	p.Logger.Info("control panel: someone added", "admin", req.Admin)
	writeJSON(w, http.StatusOK, personJSON{ID: u.Subject, Email: u.Email, Admin: u.Admin, HasPassword: true,
		Confirmed: true, MustChange: req.MustChange, CreatedAt: u.Created.Format("2006-01-02T15:04:05Z07:00")})
}

// setPersonPassword gives someone a new password (they forgot theirs, and
// no email can bring them a link): their logins end, and they choose
// their own at their next sign-in unless the admin says otherwise.
func (p *Panel) setPersonPassword(w http.ResponseWriter, r *http.Request, _ *session) {
	var req struct {
		Password   string `json:"password"`
		MustChange bool   `json:"mustChange"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		invalid(w, "not a valid password")
		return
	}
	u, ok := p.person(w, r)
	if !ok {
		return
	}
	hash, ok := p.newPassword(w, r, req.Password)
	if !ok {
		return
	}
	ctx := r.Context()
	err := p.Store.SetPassword(ctx, u.Subject, hash)
	if err == nil {
		err = p.Store.SetMustChange(ctx, u.Subject, req.MustChange)
	}
	if err == nil && !u.Confirmed {
		_, err = p.Store.ConfirmUser(ctx, u.Subject) // the admin vouches for them
	}
	if err == nil {
		err = p.Store.DeleteLogins(ctx, u.Subject)
	}
	if err != nil {
		p.fail(w, "setting a password", err)
		return
	}
	p.Logger.Info("control panel: someone's password was set")
	writeJSON(w, http.StatusOK, map[string]bool{"mustChange": req.MustChange})
}

// newPassword checks a password against the rules and hashes it,
// answering what it lacks.
func (p *Panel) newPassword(w http.ResponseWriter, r *http.Request, pw string) (string, bool) {
	rules, err := p.Store.PasswordRules(r.Context())
	if err != nil {
		p.fail(w, "reading the password rules", err)
		return "", false
	}
	switch rules.Lacks(pw) {
	case "length":
		invalid(w, fmt.Sprintf("The password needs at least %d characters.", rules.MinLength))
		return "", false
	case "digit":
		invalid(w, "The password needs a digit.")
		return "", false
	case "special":
		invalid(w, "The password needs a special character.")
		return "", false
	}
	hash, err := password.Hash(pw)
	if err != nil {
		p.fail(w, "hashing a password", err)
		return "", false
	}
	return hash, true
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
