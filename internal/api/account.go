// Package api holds Overfin's HTTP endpoints, grouped as plugins.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/C0PT3R/overfin-gateway/internal/feature"
	"github.com/C0PT3R/overfin-gateway/internal/identity"
	"github.com/C0PT3R/overfin-gateway/internal/images"
	"github.com/C0PT3R/overfin-gateway/internal/pin"
	"github.com/C0PT3R/overfin-gateway/internal/plugin"
	"github.com/C0PT3R/overfin-gateway/internal/problem"
	"github.com/C0PT3R/overfin-gateway/internal/provision"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

// Account serves the current account.
type Account struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Logger      *slog.Logger
	// KeepOnDelete disables a deleted account's Jellyfin users, keeping
	// their history, instead of deleting them (accounts.on_delete).
	KeepOnDelete bool
	// Images, when set, forgets a deleted account's image access.
	Images *images.Pipeline
	// PINs checks a profile's PIN before it downloads its own data.
	PINs *pin.Checker
	// Mounts are the modules switched on, for their say in creating
	// accounts and their part of "Download my data".
	Mounts []Mount
}

func (Account) Name() string { return "account" }

func (a Account) Register(r *plugin.Registrar) error {
	if err := r.HandleFunc("GET /api/v1/me", a.me); err != nil {
		return err
	}
	if err := r.HandleFunc("POST /api/v1/me", a.create); err != nil {
		return err
	}
	if err := r.HandleFunc("DELETE /api/v1/me", a.deleteAccount); err != nil {
		return err
	}
	return r.HandleFunc("POST /api/v1/me/export", a.export)
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
	AccountID string `json:"accountId"`
	// ActiveProfile is absent while this session has none chosen.
	ActiveProfile *profileJSON `json:"activeProfile,omitempty"`
}

func (a Account) me(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context()) // the registrar guarantees one
	if len(a.Sessions.SetupOffers) == 0 {
		// With nothing else on offer, the first visit creates the account.
		if _, _, err := a.Provisioner.Ensure(r.Context(), id, creationHooks(r, a.Mounts, a.Provisioner)); err != nil {
			a.respond(w, nil, nil, err, http.StatusOK)
			return
		}
	}
	c := a.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	a.respond(w, c.Account, c.Profile, nil, http.StatusOK)
}

func (a Account) create(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context())
	acct, prof, err := a.Provisioner.Create(r.Context(), id, creationHooks(r, a.Mounts, a.Provisioner))
	a.respond(w, acct, prof, err, http.StatusCreated)
}

func (a Account) respond(w http.ResponseWriter, acct *store.Account, prof *store.Profile, err error, status int) {
	accountResult(w, a.Sessions, a.Logger, acct, prof, err, status)
}

// accountResult maps provisioning results to the API. Errors never carry
// details.
func accountResult(w http.ResponseWriter, sessions *Sessions, logger *slog.Logger, acct *store.Account, prof *store.Profile, err error, status int) {
	var refusal *feature.Refusal // a module refused the account
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		out := meJSON{AccountID: acct.ID}
		if prof != nil {
			pj := toProfileJSON(prof)
			out.ActiveProfile = &pj
		}
		_ = json.NewEncoder(w).Encode(out)
	case errors.As(err, &refusal):
		problem.Send(w, refusal.Type, refusal.Detail)
	case errors.Is(err, provision.ErrNoAccount):
		sessions.setupRequired(w)
	case errors.Is(err, provision.ErrAccountExists):
		problem.Send(w, problem.AccountExists, "")
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
	case errors.Is(err, provision.ErrExpired):
		problem.Send(w, problem.AccountExpired, "")
	case errors.Is(err, provision.ErrClaimFailed):
		problem.Send(w, problem.ClaimFailed, "")
	case errors.Is(err, provision.ErrClaimNotAllowed):
		problem.Send(w, problem.ClaimNotAllowed, "")
	case errors.Is(err, provision.ErrRateLimited):
		problem.Send(w, problem.RateLimited, "")
	case errors.Is(err, provision.ErrAnonymous):
		problem.Send(w, problem.NotFound, "")
	default:
		logger.Error("account request failed", "err", err)
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
