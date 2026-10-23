// Package api holds Overfin's HTTP endpoints, grouped as plugins.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/lang"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/pin"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
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
	if err := r.HandleFunc("PATCH /api/v1/me", a.change); err != nil {
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
	// MovieCeiling and SeriesCeiling are a kids profile's highest allowed
	// certifications; CeilingsNeeded, that it has none yet (it sees
	// nothing until its owner picks them).
	MovieCeiling   string `json:"movieCeiling,omitempty"`
	SeriesCeiling  string `json:"seriesCeiling,omitempty"`
	CeilingsNeeded bool   `json:"ceilingsNeeded,omitempty"`
	// Libraries, when present, are the only libraries the profile gets;
	// absent, it gets every library. BlockedTags are hidden from it.
	Libraries   *[]string `json:"libraries,omitempty"`
	BlockedTags []string  `json:"blockedTags,omitempty"`
}

type meJSON struct {
	AccountID string `json:"accountId"`
	// ActiveProfile is absent while this session has none chosen.
	ActiveProfile *profileJSON `json:"activeProfile,omitempty"`
	// Language is the account's (a regional tag such as "fr-CA"), and
	// Languages those it may choose among.
	Language  string        `json:"language"`
	Languages []lang.Choice `json:"languages"`
}

func (a Account) me(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context()) // the registrar guarantees one
	if len(a.Sessions.SetupOffers) == 0 {
		// With nothing else on offer, the first visit creates the account.
		if _, _, err := a.Provisioner.Ensure(r.Context(), id, creationHooks(r, a.Mounts, a.Provisioner)); err != nil {
			a.respond(w, r, nil, nil, err, http.StatusOK)
			return
		}
	}
	c := a.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	a.respond(w, r, c.Account, c.Profile, nil, http.StatusOK)
}

func (a Account) create(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context())
	acct, prof, err := a.Provisioner.Create(r.Context(), id, creationHooks(r, a.Mounts, a.Provisioner))
	a.respond(w, r, acct, prof, err, http.StatusCreated)
}

// change changes the account's language, among those offered. It's the
// whole account's: the owner profile's to change (SECURITY.md rule 25).
func (a Account) change(w http.ResponseWriter, r *http.Request) {
	c := a.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	var req struct {
		Language string `json:"language"`
	}
	if !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	offered, _, _, err := a.Sessions.Store.Languages(r.Context())
	if err == nil && !slices.Contains(offered, req.Language) {
		problem.Send(w, problem.InvalidRequest, "not one of the languages offered")
		return
	}
	if err == nil {
		err = a.Sessions.Store.SetAccountLanguage(r.Context(), c.Account.ID, req.Language)
	}
	if err != nil {
		a.Logger.Error("changing an account's language failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	c.Account.Language = req.Language
	a.respond(w, r, c.Account, c.Profile, nil, http.StatusOK)
}

func (a Account) respond(w http.ResponseWriter, r *http.Request, acct *store.Account, prof *store.Profile, err error, status int) {
	accountResult(w, r, a.Sessions, a.Logger, acct, prof, err, status)
}

// accountResult maps provisioning results to the API. Errors never carry
// details.
func accountResult(w http.ResponseWriter, r *http.Request, sessions *Sessions, logger *slog.Logger, acct *store.Account, prof *store.Profile, err error, status int) {
	var refusal *feature.Refusal // a module refused the account
	var out meJSON
	if err == nil {
		out, err = sessions.me(r, acct, prof)
	}
	switch {
	case err == nil:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	case errors.As(err, &refusal):
		problem.Send(w, refusal.Type, refusal.Detail)
	case errors.Is(err, provision.ErrNoAccount):
		sessions.setupRequired(w, r)
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
	out := profileJSON{ID: p.ID, Name: p.Name, Kind: p.Kind, Owner: p.Owner, HasPIN: p.HasPIN,
		MovieCeiling: p.MovieCeiling, SeriesCeiling: p.SeriesCeiling,
		CeilingsNeeded: p.Kind == "kids" && (p.MovieCeiling == "" || p.SeriesCeiling == ""),
		BlockedTags:    p.BlockedTags}
	if p.Libraries != nil {
		libs := make([]string, 0, len(p.Libraries))
		for _, id := range p.Libraries {
			libs = append(libs, library.LibraryPrefix+id)
		}
		out.Libraries = &libs
	}
	return out
}

// me is what GET /api/v1/me answers. An account without a language yet
// gets one: the visitor's browser's first among those offered, else the
// default.
func (s *Sessions) me(r *http.Request, acct *store.Account, prof *store.Profile) (meJSON, error) {
	ctx := r.Context()
	offered, def, _, err := s.Store.Languages(ctx)
	if err != nil {
		return meJSON{}, err
	}
	if acct.Language == "" {
		tag := pickLanguage(r.Header.Get("Accept-Language"), offered, def)
		if err := s.Store.SetAccountLanguage(ctx, acct.ID, tag); err != nil {
			return meJSON{}, err
		}
		acct.Language = tag
	}
	out := meJSON{AccountID: acct.ID, Language: def, Languages: []lang.Choice{}}
	if slices.Contains(offered, acct.Language) {
		out.Language = acct.Language
	}
	for _, c := range lang.Choices {
		if slices.Contains(offered, c.Tag) {
			out.Languages = append(out.Languages, c)
		}
	}
	if prof != nil {
		pj := toProfileJSON(prof)
		out.ActiveProfile = &pj
	}
	return out, nil
}

// pickLanguage is the first of the browser's languages (Accept-Language)
// among those offered: the same tag, else the same language; def when
// none is.
func pickLanguage(accept string, offered []string, def string) string {
	for _, part := range strings.Split(accept, ",") {
		want, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if want == "" {
			continue
		}
		for _, tag := range offered {
			if strings.EqualFold(tag, want) {
				return tag
			}
		}
		for _, tag := range offered {
			if lang.Same(tag, want) {
				return tag
			}
		}
	}
	return def
}
