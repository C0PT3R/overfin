package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/C0PT3R/overfin-server/internal/connector"
	"github.com/C0PT3R/overfin-server/internal/store"
	"github.com/C0PT3R/overfin-server/internal/textclean"
)

// Users: every account with its profiles and devices, and what the operator
// can do about them.

type accountJSON struct {
	ID string `json:"id"`
	// Subject is the identity provider's ID for the person, to look them up
	// there until a connector shows names.
	Subject   string      `json:"subject"`
	Label     string      `json:"label,omitempty"`
	CreatedAt string      `json:"createdAt"`
	Status    string      `json:"status"` // active, disabled, expired
	ExpiresAt string      `json:"expiresAt,omitempty"`
	Template  templateRef `json:"template"`
	// Person is who the identity provider says this is, live from the
	// connector; absent without one, or when the provider doesn't know.
	Person   *connector.Person `json:"person,omitempty"`
	Profiles []adminProfile    `json:"profiles"`
	Sessions []adminSession    `json:"sessions"`
}

type adminProfile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Owner         bool   `json:"owner"`
	RatingCeiling string `json:"ratingCeiling,omitempty"`
	HasPIN        bool   `json:"hasPin"`
	PINLocked     bool   `json:"pinLocked"`
	// Linked: adopted in "link" mode, so its Jellyfin login still works.
	Linked bool `json:"linked"`
}

type adminSession struct {
	ID           string `json:"id"`
	Device       string `json:"device,omitempty"`
	Profile      string `json:"profile,omitempty"`
	CreatedAt    string `json:"createdAt"`
	LastActiveAt string `json:"lastActiveAt"`
}

func (p *Panel) accountJSON(r *http.Request, a *store.Account) (accountJSON, error) {
	ctx := r.Context()
	out := accountJSON{ID: a.ID, Subject: a.Subject, Label: a.Label, CreatedAt: a.Created.Format(time.RFC3339),
		Status: "active", Template: templateRef{ID: a.Template.ID, Name: a.Template.Name},
		Profiles: []adminProfile{}, Sessions: []adminSession{}}
	switch {
	case a.Disabled:
		out.Status = "disabled"
	case a.Expired(p.clock()):
		out.Status = "expired"
	}
	if !a.Expires.IsZero() {
		out.ExpiresAt = a.Expires.Format(time.RFC3339)
	}
	profiles, err := p.Store.Profiles(ctx, a.ID)
	if err != nil {
		return out, err
	}
	locks, err := p.Store.PINLocks(ctx, a.ID)
	if err != nil {
		return out, err
	}
	names := map[string]string{}
	ownerName := ""
	for _, pr := range profiles {
		names[pr.ID] = pr.Name
		if pr.Owner {
			ownerName = pr.Name
		}
		out.Profiles = append(out.Profiles, adminProfile{ID: pr.ID, Name: pr.Name, Kind: pr.Kind, Owner: pr.Owner,
			RatingCeiling: pr.RatingCeiling, HasPIN: pr.HasPIN, PINLocked: locks[pr.ID],
			Linked: !strings.HasPrefix(pr.JellyfinUsername, "of-")})
	}
	sessions, err := p.Store.Sessions(ctx, a.ID)
	if err != nil {
		return out, err
	}
	for _, s := range sessions {
		name := names[s.ActiveProfileID]
		if s.ActiveProfileID == "" {
			name = ownerName + " (no profile chosen)"
		}
		out.Sessions = append(out.Sessions, adminSession{ID: s.ID, Device: s.Device, Profile: name,
			CreatedAt: s.Created.Format(time.RFC3339), LastActiveAt: s.LastActive.Format(time.RFC3339)})
	}
	return out, nil
}

func (p *Panel) accounts(w http.ResponseWriter, r *http.Request, _ *session) {
	all, err := p.Store.Accounts(r.Context())
	if err != nil {
		p.fail(w, "listing accounts", err)
		return
	}
	// Who's who, from the connector. Without it, or if it fails, the list
	// still works, and says why names are missing.
	directory := "none"
	people, err := p.directory(r.Context())
	switch {
	case p.Directory == nil:
	case err != nil:
		directory = "unreachable"
		p.Logger.Warn("control panel: the connector failed", "err", err)
	default:
		directory = "ok"
	}
	out := []accountJSON{}
	for _, a := range all {
		aj, err := p.accountJSON(r, a)
		if err != nil {
			p.fail(w, "listing accounts", err)
			return
		}
		if person, ok := people[a.Subject]; ok {
			aj.Person = &person
		}
		out = append(out, aj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "directory": directory})
}

type accountChange struct {
	Label    *string `json:"label"`
	Disabled *bool   `json:"disabled"`
	// ExpiresAt is an RFC 3339 time, or "" for never.
	ExpiresAt *string `json:"expiresAt"`
	// TemplateID puts the account on another account template.
	TemplateID *string `json:"templateId"`
}

func (p *Panel) changeAccount(w http.ResponseWriter, r *http.Request, _ *session) {
	ctx := r.Context()
	var c accountChange
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		invalid(w, "not a valid change")
		return
	}
	var expires time.Time
	if c.ExpiresAt != nil && *c.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *c.ExpiresAt)
		if err != nil {
			invalid(w, "expiresAt must be a date and time (RFC 3339), or empty")
			return
		}
		expires = t
	}
	if c.Label != nil && textclean.Plain(*c.Label, 64) != *c.Label {
		invalid(w, "a label is plain text, 64 characters at most")
		return
	}
	id := r.PathValue("id")
	a, err := p.Store.AccountByID(ctx, id)
	if err != nil {
		http.Error(w, "No such account", http.StatusNotFound)
		return
	}
	if c.Label != nil {
		err = errors.Join(err, p.Store.SetAccountLabel(ctx, a.ID, *c.Label))
	}
	if c.Disabled != nil {
		err = errors.Join(err, p.Store.SetAccountDisabled(ctx, a.ID, *c.Disabled))
	}
	if c.ExpiresAt != nil {
		err = errors.Join(err, p.Store.SetAccountExpiry(ctx, a.ID, expires))
	}
	if c.TemplateID != nil {
		if terr := p.Store.SetAccountTemplate(ctx, a.ID, *c.TemplateID); errors.Is(terr, store.ErrNotFound) {
			invalid(w, "no such template")
			return
		} else {
			err = errors.Join(err, terr)
		}
	}
	if err != nil {
		p.fail(w, "changing an account", err)
		return
	}
	if a, err = p.Store.AccountByID(ctx, id); err != nil {
		p.fail(w, "changing an account", err)
		return
	}
	aj, err := p.accountJSON(r, a)
	if err != nil {
		p.fail(w, "changing an account", err)
		return
	}
	p.Logger.Info("account changed in the control panel", "account", a.ID)
	writeJSON(w, http.StatusOK, aj)
}

// deleteAccount deletes an account like its owner would: its Jellyfin users
// deleted with their history, or kept disabled, as the instance is set.
func (p *Panel) deleteAccount(w http.ResponseWriter, r *http.Request, _ *session) {
	keep := p.Features.Features().Accounts.OnDelete == "disable"
	if _, err := p.Provisioner.DeleteAccount(r.Context(), r.PathValue("id"), keep); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such account", http.StatusNotFound)
		return
	} else if err != nil {
		p.fail(w, "deleting an account", err)
		return
	}
	history := "deleted"
	if keep {
		history = "kept"
	}
	writeJSON(w, http.StatusOK, map[string]string{"history": history})
}

func (p *Panel) unlockPIN(w http.ResponseWriter, r *http.Request, _ *session) {
	if err := p.Store.UnlockPIN(r.Context(), r.PathValue("id")); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such profile", http.StatusNotFound)
		return
	} else if err != nil {
		p.fail(w, "unlocking a PIN", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) endSession(w http.ResponseWriter, r *http.Request, _ *session) {
	err := p.Store.DeleteSession(r.Context(), r.PathValue("id"), r.PathValue("session"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "No such session", http.StatusNotFound)
		return
	} else if err != nil {
		p.fail(w, "ending a session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *Panel) fail(w http.ResponseWriter, what string, err error) {
	p.Logger.Error("control panel: "+what+" failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal-error"})
}

func invalid(w http.ResponseWriter, detail string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": detail})
}

type templateRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
