package api

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
	"github.com/C0PT3R/overfin/internal/textclean"
)

// Profiles serves profiles ("Who's watching?") and sessions.
type Profiles struct {
	Sessions    *Sessions
	Provisioner *provision.Provisioner
	Store       *store.Store
	MaxProfiles int
	// LogoutURL is where the site sends a user to also leave the identity
	// provider; empty when there is none.
	LogoutURL string
	Logger    *slog.Logger
}

func (Profiles) Name() string { return "profiles" }

func (p Profiles) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/profiles":                p.list,
		"POST /api/v1/profiles":               p.create,
		"GET /api/v1/profiles/{id}":           p.get,
		"PATCH /api/v1/profiles/{id}":         p.update,
		"POST /api/v1/profiles/{id}/activate": p.activate,
		"GET /api/v1/sessions":                p.sessions,
		"DELETE /api/v1/sessions":             p.endOtherSessions,
		"DELETE /api/v1/sessions/{id}":        p.endSession,
		"POST /api/v1/logout":                 p.logout,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

func (p Profiles) list(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	all, err := p.Store.Profiles(r.Context(), c.Account.ID)
	if err != nil {
		p.Logger.Error("listing profiles failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	out := []profileJSON{}
	for _, pr := range all {
		out = append(out, toProfileJSON(pr))
	}
	writeOK(w, map[string]any{"items": out})
}

func (p Profiles) get(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	pr, err := p.Store.Profile(r.Context(), c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

type profileRequest struct {
	Name     *string `json:"name"`
	Kind     *string `json:"kind"`
	Language *string `json:"language"`
}

var languageTagRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)

// validName accepts 1 to 32 bytes of plain text that is already clean.
func validName(s string) bool {
	return s != "" && len(s) <= 32 && utf8.ValidString(s) && textclean.Plain(s, 32) == s
}

func (p Profiles) create(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(r, &req) || req.Name == nil || !validName(*req.Name) ||
		(req.Language != nil && !languageTagRe.MatchString(*req.Language)) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	kind := "standard"
	if req.Kind != nil {
		kind = *req.Kind
	}
	if kind != "standard" {
		// Kids profiles arrive with rating ceilings and PINs.
		problem.Send(w, problem.InvalidRequest, "only standard profiles for now")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	np := store.NewProfile{Name: *req.Name, Kind: kind}
	if req.Language != nil {
		np.Language = *req.Language
	}
	prof, err := p.Provisioner.AddProfile(r.Context(), c.Account.ID, np, p.MaxProfiles)
	switch {
	case errors.Is(err, store.ErrNameTaken):
		problem.Send(w, problem.NameTaken, "")
	case errors.Is(err, store.ErrProfileLimit):
		problem.Send(w, problem.ProfileLimitReached, "")
	case err != nil:
		p.Logger.Error("creating a profile failed", "err", err)
		problem.Send(w, problem.Internal, "")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeOK(w, toProfileJSON(prof))
	}
}

func (p Profiles) update(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(r, &req) || req.Kind != nil || (req.Name == nil && req.Language == nil) ||
		(req.Name != nil && !validName(*req.Name)) ||
		(req.Language != nil && *req.Language != "" && !languageTagRe.MatchString(*req.Language)) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := p.Store.Profile(ctx, c.Account.ID, id); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if req.Name != nil {
		if err := p.Store.RenameProfile(ctx, c.Account.ID, id, *req.Name); errors.Is(err, store.ErrNameTaken) {
			problem.Send(w, problem.NameTaken, "")
			return
		} else if err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	if req.Language != nil {
		if err := p.Store.SetProfileLanguage(ctx, c.Account.ID, id, *req.Language); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
	}
	pr, err := p.Store.Profile(ctx, c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

// activate switches this session to a profile. PINs arrive with kids
// profiles; until then no profile has one.
func (p Profiles) activate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN string `json:"pin"`
	}
	if r.ContentLength != 0 && !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	id := r.PathValue("id")
	pr, err := p.Store.Profile(r.Context(), c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if err := p.Store.SetActiveProfile(r.Context(), c.Session.ID, c.Account.ID, id); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

// ---- Sessions ----------------------------------------------------------------------

type sessionJSONOut struct {
	ID           string `json:"id"`
	Current      bool   `json:"current"`
	Device       string `json:"device,omitempty"`
	ProfileName  string `json:"profileName,omitempty"`
	CreatedAt    string `json:"createdAt"`
	LastActiveAt string `json:"lastActiveAt,omitempty"`
}

// sessions lists sessions: all of the account's for the owner profile, and
// only those currently using their own profile for anyone else (rule 51).
func (p Profiles) sessions(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	all, err := p.Store.Sessions(ctx, c.Account.ID)
	if err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	names := map[string]string{}
	if c.IsOwner() {
		profiles, _ := p.Store.Profiles(ctx, c.Account.ID)
		for _, pr := range profiles {
			names[pr.ID] = pr.Name
		}
	}
	out := []sessionJSONOut{}
	for _, s := range all {
		active := s.ActiveProfileID
		if active == "" {
			active = c.Owner.ID
		}
		if !c.IsOwner() && active != c.Profile.ID {
			continue
		}
		out = append(out, sessionJSONOut{
			ID: s.ID, Current: s.ID == c.Session.ID, Device: s.Device, ProfileName: names[active],
			CreatedAt: s.Created.Format(time.RFC3339), LastActiveAt: s.LastActive.Format(time.RFC3339),
		})
	}
	writeOK(w, map[string]any{"items": out})
}

func (p Profiles) endSession(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	target, err := p.findSession(r, c, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if err := p.Store.DeleteSession(ctx, c.Account.ID, target.ID); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if target.ID == c.Session.ID {
		clearCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// findSession returns a session the caller may see: any of the account's
// for the owner, only those using the caller's profile otherwise.
func (p Profiles) findSession(r *http.Request, c *Caller, id string) (*store.Session, error) {
	all, err := p.Store.Sessions(r.Context(), c.Account.ID)
	if err != nil {
		return nil, err
	}
	for _, s := range all {
		active := s.ActiveProfileID
		if active == "" {
			active = c.Owner.ID
		}
		if s.ID == id && (c.IsOwner() || active == c.Profile.ID) {
			return s, nil
		}
	}
	return nil, store.ErrNotFound
}

func (p Profiles) endOtherSessions(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if err := p.Store.DeleteOtherSessions(r.Context(), c.Account.ID, c.Session.ID); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p Profiles) logout(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	_ = p.Store.DeleteSession(r.Context(), c.Account.ID, c.Session.ID)
	clearCookie(w)
	out := map[string]string{}
	if p.LogoutURL != "" {
		out["logoutUrl"] = p.LogoutURL
	}
	writeOK(w, out)
}
