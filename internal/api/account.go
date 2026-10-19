// Package api holds Overfin's HTTP endpoints, grouped as plugins.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
)

// Account serves the current account: /me for now.
type Account struct {
	Provisioner *provision.Provisioner
	Logger      *slog.Logger
}

func (Account) Name() string { return "account" }

func (a Account) Register(r *plugin.Registrar) error {
	return r.HandleFunc("GET /api/v1/me", a.me)
}

// Shapes from the API specification.
type profileJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Owner  bool   `json:"owner"`
	HasPIN bool   `json:"hasPin"`
}

type meJSON struct {
	AccountID     string      `json:"accountId"`
	ActiveProfile profileJSON `json:"activeProfile"`
}

func (a Account) me(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context()) // the registrar guarantees one
	acct, prof, err := a.Provisioner.Ensure(r.Context(), id)
	switch {
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
		return
	case errors.Is(err, provision.ErrRateLimited):
		problem.Send(w, problem.RateLimited, "")
		return
	case errors.Is(err, provision.ErrAnonymous):
		problem.Send(w, problem.NotFound, "")
		return
	case err != nil:
		a.Logger.Error("account lookup failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	writeJSON(w, meJSON{AccountID: acct.ID, ActiveProfile: toProfileJSON(prof)})
}

func toProfileJSON(p *store.Profile) profileJSON {
	return profileJSON{ID: p.ID, Name: p.Name, Kind: p.Kind, Owner: p.Owner, HasPIN: p.HasPIN}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
