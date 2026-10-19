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

// Account serves the current account.
type Account struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Logger      *slog.Logger
	// Claim enables adopting an existing Jellyfin account. When it's on, a
	// first visit asks the user instead of creating an account silently.
	Claim bool
}

func (Account) Name() string { return "account" }

func (a Account) Register(r *plugin.Registrar) error {
	if err := r.HandleFunc("GET /api/v1/me", a.me); err != nil {
		return err
	}
	if err := r.HandleFunc("POST /api/v1/me", a.create); err != nil {
		return err
	}
	if a.Claim {
		return r.HandleFunc("POST /api/v1/me/claim", a.claim)
	}
	return nil
}

// Shapes from the API specification.
type profileJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Owner  bool   `json:"owner"`
	HasPIN bool   `json:"hasPin"`
	// RatingCeiling is a kids profile's highest allowed rating.
	RatingCeiling string `json:"ratingCeiling,omitempty"`
}

type meJSON struct {
	AccountID     string      `json:"accountId"`
	ActiveProfile profileJSON `json:"activeProfile"`
}

func (a Account) me(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context()) // the registrar guarantees one
	if !a.Claim {
		// Without claiming, the first visit creates the account.
		if _, _, err := a.Provisioner.Ensure(r.Context(), id); err != nil {
			a.respond(w, nil, nil, err, http.StatusOK)
			return
		}
	}
	c := a.Sessions.Current(w, r)
	if c == nil {
		return
	}
	a.respond(w, c.Account, c.Profile, nil, http.StatusOK)
}

func (a Account) create(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context())
	acct, prof, err := a.Provisioner.Create(r.Context(), id)
	a.respond(w, acct, prof, err, http.StatusCreated)
}

type claimRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a Account) claim(w http.ResponseWriter, r *http.Request) {
	var req claimRequest
	if !decode(r, &req) || req.Username == "" || len(req.Username) > 128 ||
		req.Password == "" || len(req.Password) > 256 {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	id, _ := identity.FromContext(r.Context())
	acct, prof, err := a.Provisioner.Claim(r.Context(), id, req.Username, req.Password)
	a.respond(w, acct, prof, err, http.StatusCreated)
}

// respond maps provisioning results to the API. Errors never carry details.
func (a Account) respond(w http.ResponseWriter, acct *store.Account, prof *store.Profile, err error, status int) {
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(meJSON{AccountID: acct.ID, ActiveProfile: toProfileJSON(prof)})
	case errors.Is(err, provision.ErrNoAccount):
		a.Sessions.setupRequired(w)
	case errors.Is(err, provision.ErrAccountExists):
		problem.Send(w, problem.AccountExists, "")
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
	case errors.Is(err, provision.ErrClaimFailed):
		problem.Send(w, problem.ClaimFailed, "")
	case errors.Is(err, provision.ErrClaimNotAllowed):
		problem.Send(w, problem.ClaimNotAllowed, "")
	case errors.Is(err, provision.ErrRateLimited):
		problem.Send(w, problem.RateLimited, "")
	case errors.Is(err, provision.ErrAnonymous):
		problem.Send(w, problem.NotFound, "")
	default:
		a.Logger.Error("account request failed", "err", err)
		problem.Send(w, problem.Internal, "")
	}
}

// decode reads a small JSON body strictly: unknown fields and trailing data
// are refused. It reports false on any problem.
func decode(r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return false
	}
	return !dec.More()
}

func toProfileJSON(p *store.Profile) profileJSON {
	return profileJSON{ID: p.ID, Name: p.Name, Kind: p.Kind, Owner: p.Owner, HasPIN: p.HasPIN, RatingCeiling: p.RatingCeiling}
}
