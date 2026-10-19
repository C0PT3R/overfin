package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/C0PT3R/overfin/internal/images"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/pin"
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
	PINs        *pin.Checker
	// Images, when set, forgets a profile's image access when its limits
	// change.
	Images      *images.Pipeline
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
		"DELETE /api/v1/profiles/{id}":        p.remove,
		"POST /api/v1/profiles/{id}/activate": p.activate,
		"PUT /api/v1/profiles/{id}/pin":       p.setPIN,
		"DELETE /api/v1/profiles/{id}/pin":    p.removePIN,
		"GET /api/v1/ratings":                 p.ratings,
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
	c := p.Sessions.CurrentAccount(w, r)
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
	Name          *string `json:"name"`
	Kind          *string `json:"kind"`
	Language      *string `json:"language"`
	RatingCeiling *string `json:"ratingCeiling"`
	// PIN is the owner's PIN, needed to change a profile's limits when the
	// owner has one.
	PIN string `json:"pin"`
}

var languageTagRe = regexp.MustCompile(`^[a-zA-Z]{2,3}(-[a-zA-Z0-9]{2,8})*$`)

// validName accepts 1 to 32 bytes of plain text that is already clean.
func validName(s string) bool {
	return s != "" && len(s) <= 32 && utf8.ValidString(s) && textclean.Plain(s, 32) == s
}

// errUnknownRating: a rating ceiling the server doesn't know.
var errUnknownRating = errors.New("unknown rating")

// rating finds a rating by name among the server's ratings.
func (p Profiles) rating(ctx context.Context, name string) (*jellyfin.Rating, error) {
	all, err := p.Provisioner.Ratings(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range all {
		if r.Name == name {
			return &r, nil
		}
	}
	return nil, errUnknownRating
}

// ratings lists the rating names a kids profile's ceiling can be, from least
// to most mature.
func (p Profiles) ratings(w http.ResponseWriter, r *http.Request) {
	if p.Sessions.Current(w, r) == nil {
		return
	}
	all, err := p.Provisioner.Ratings(r.Context())
	if err != nil {
		p.Logger.Error("listing ratings failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	type ratingJSON struct {
		Name string `json:"name"`
	}
	out := []ratingJSON{}
	seen := map[string]bool{}
	for _, rt := range all {
		if !seen[rt.Name] && validName(rt.Name) {
			seen[rt.Name] = true
			out = append(out, ratingJSON{rt.Name})
		}
	}
	writeOK(w, map[string]any{"items": out})
}

func (p Profiles) create(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !decode(r, &req) || req.Name == nil || !validName(*req.Name) || req.PIN != "" ||
		(req.Language != nil && !languageTagRe.MatchString(*req.Language)) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	kind := "standard"
	if req.Kind != nil {
		kind = *req.Kind
	}
	// A kids profile needs a ceiling; any other profile can't have one.
	if (kind != "standard" && kind != "kids") || (kind == "kids") != (req.RatingCeiling != nil) {
		problem.Send(w, problem.InvalidRequest, "")
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
	var ceiling *jellyfin.Rating
	if req.RatingCeiling != nil {
		var err error
		if ceiling, err = p.rating(r.Context(), *req.RatingCeiling); errors.Is(err, errUnknownRating) {
			problem.Send(w, problem.InvalidRequest, "unknown rating")
			return
		} else if err != nil {
			p.Logger.Error("listing ratings failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return
		}
		np.RatingCeiling = ceiling.Name
	}
	prof, err := p.Provisioner.AddProfile(r.Context(), c.Account.ID, np, ceiling, p.MaxProfiles)
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
	if !decode(r, &req) ||
		(req.Name == nil && req.Language == nil && req.Kind == nil && req.RatingCeiling == nil) ||
		(req.Name != nil && !validName(*req.Name)) ||
		(req.Language != nil && *req.Language != "" && !languageTagRe.MatchString(*req.Language)) ||
		(req.Kind != nil && *req.Kind != "standard" && *req.Kind != "kids") {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	pr, err := p.Store.Profile(ctx, c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	// Limits first: they can fail on the PIN, and nothing else should
	// change then.
	if req.Kind != nil || req.RatingCeiling != nil {
		if !p.changeLimits(w, r, c, pr, req) {
			return
		}
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
	if pr, err = p.Store.Profile(ctx, c.Account.ID, id); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	writeOK(w, toProfileJSON(pr))
}

// changeLimits makes a profile a kids profile, changes its rating ceiling, or
// makes it a standard profile again. Only the owner can, with the owner's PIN
// if there is one. It writes the problem and returns false on failure.
func (p Profiles) changeLimits(w http.ResponseWriter, r *http.Request, c *Caller, pr *store.Profile, req profileRequest) bool {
	ctx := r.Context()
	if pr.Owner {
		problem.Send(w, problem.InvalidRequest, "the owner profile has no limits")
		return false
	}
	kind := pr.Kind
	if req.Kind != nil {
		kind = *req.Kind
	}
	name := pr.RatingCeiling
	if req.RatingCeiling != nil {
		name = *req.RatingCeiling
	}
	if kind == "standard" {
		if req.RatingCeiling != nil {
			problem.Send(w, problem.InvalidRequest, "a standard profile has no rating ceiling")
			return false
		}
		name = ""
	}
	var ceiling *jellyfin.Rating
	if kind == "kids" {
		var err error
		if ceiling, err = p.rating(ctx, name); errors.Is(err, errUnknownRating) {
			problem.Send(w, problem.InvalidRequest, "unknown rating")
			return false
		} else if err != nil {
			p.Logger.Error("listing ratings failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return false
		}
	}
	if !p.checkPIN(w, ctx, c.Account.ID, c.Owner.ID, req.PIN) {
		return false
	}

	// Whatever fails half-way must leave the profile limited: Jellyfin
	// enforces the limit, so it is tightened before the record changes,
	// and loosened only after.
	restrict := func() bool {
		if err := p.Provisioner.Restrict(ctx, pr.JellyfinUserID, ceiling); err != nil {
			p.Logger.Error("changing a profile's limits failed", "err", err)
			problem.Send(w, problem.Internal, "")
			return false
		}
		return true
	}
	record := func() bool {
		if err := p.Store.SetKind(ctx, c.Account.ID, pr.ID, kind, name); err != nil {
			problem.Send(w, problem.NotFound, "")
			return false
		}
		return true
	}
	ok := false
	if ceiling != nil {
		ok = restrict() && record()
	} else {
		ok = record() && restrict()
	}
	if p.Images != nil {
		p.Images.Forget(pr.ID)
	}
	return ok
}

// checkPIN checks a profile's PIN. It writes the problem and returns false
// when the PIN is missing, wrong or can't be tried yet.
func (p Profiles) checkPIN(w http.ResponseWriter, ctx context.Context, accountID, profileID, given string) bool {
	return checkPIN(w, ctx, p.PINs, p.Logger, accountID, profileID, given)
}

// checkPIN checks a profile's PIN with checker, writing the problem and
// returning false when it's missing, wrong, or can't be tried yet.
func checkPIN(w http.ResponseWriter, ctx context.Context, checker *pin.Checker, logger *slog.Logger, accountID, profileID, given string) bool {
	err := checker.Check(ctx, accountID, profileID, given)
	var wait *pin.WaitError
	switch {
	case err == nil:
		return true
	case errors.Is(err, pin.ErrRequired):
		problem.Send(w, problem.PINRequired, "")
	case errors.Is(err, pin.ErrIncorrect):
		problem.Send(w, problem.PINIncorrect, "")
	case errors.As(err, &wait):
		t := problem.RateLimited
		if wait.Locked {
			t = problem.PINLocked
		}
		pb := problem.New(t, "")
		pb.RetryAfter = int((wait.Wait + time.Second - 1) / time.Second)
		problem.Write(w, pb)
	case errors.Is(err, store.ErrNotFound):
		problem.Send(w, problem.NotFound, "")
	default:
		logger.Error("checking a PIN failed", "err", err)
		problem.Send(w, problem.Internal, "")
	}
	return false
}

// pinAllowed reports whether the caller may set a profile's PIN: the owner
// for any profile, a standard profile for itself. Kids profiles can't lock
// themselves.
func pinAllowed(c *Caller, pr *store.Profile) bool {
	return c.IsOwner() || (pr.ID == c.Profile.ID && c.Profile.Kind == "standard")
}

// setPIN gives a profile a PIN. Changing an existing one needs a fresh login
// (rule 21) instead of the owner's PIN.
func (p Profiles) setPIN(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN      string `json:"pin"`
		OwnerPIN string `json:"ownerPin"`
	}
	if !decode(r, &req) || !pin.Valid(req.PIN) {
		problem.Send(w, problem.InvalidRequest, "a PIN is 4 to 8 digits")
		return
	}
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	pr, err := p.Store.Profile(ctx, c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !pinAllowed(c, pr) {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if pr.HasPIN {
		if !p.Sessions.RequireFresh(w, c) {
			return
		}
		if err := p.Store.ReplacePIN(ctx, c.Account.ID, pr.ID, p.PINs.Hash(pr.ID, req.PIN)); err != nil {
			problem.Send(w, problem.NotFound, "")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// The owner protecting someone else's profile proves it's the owner.
	if !pr.Owner && c.IsOwner() && !p.checkPIN(w, ctx, c.Account.ID, c.Owner.ID, req.OwnerPIN) {
		return
	}
	if err := p.Store.SetPIN(ctx, c.Account.ID, pr.ID, p.PINs.Hash(pr.ID, req.PIN)); errors.Is(err, store.ErrPINExists) {
		p.Sessions.RequireFresh(w, c) // set by someone else meanwhile
		return
	} else if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removePIN needs a fresh login (rule 21).
func (p Profiles) removePIN(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	pr, err := p.Store.Profile(r.Context(), c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !pinAllowed(c, pr) {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if !pr.HasPIN {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !p.Sessions.RequireFresh(w, c) {
		return
	}
	if err := p.Store.ReplacePIN(r.Context(), c.Account.ID, pr.ID, nil); err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// remove deletes a profile and, with its Jellyfin user, its watch history.
// Owner only, never the owner profile itself, and only after a fresh login.
func (p Profiles) remove(w http.ResponseWriter, r *http.Request) {
	c := p.Sessions.Current(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	pr, err := p.Store.Profile(ctx, c.Account.ID, r.PathValue("id"))
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return
	}
	if pr.Owner {
		problem.Send(w, problem.InvalidRequest, "the owner profile goes with the account")
		return
	}
	if !p.Sessions.RequireFresh(w, c) {
		return
	}
	// Jellyfin first: if that fails, the profile stays and can be deleted
	// again, rather than leaving a history nobody can reach or erase.
	if err := p.Provisioner.DeleteJellyfinUser(ctx, pr.JellyfinUserID); err != nil {
		p.Logger.Error("deleting a profile's Jellyfin user failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if err := p.Store.DeleteProfile(ctx, c.Account.ID, pr.ID); err != nil {
		p.Logger.Error("deleting a profile failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	if p.Images != nil {
		p.Images.Forget(pr.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// activate switches this session to a profile, with its PIN if it has one.
func (p Profiles) activate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PIN string `json:"pin"`
	}
	if r.ContentLength != 0 && !decode(r, &req) {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	c := p.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	pr, err := p.Store.Profile(ctx, c.Account.ID, id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if (c.Profile == nil || id != c.Profile.ID) && !p.checkPIN(w, ctx, c.Account.ID, id, req.PIN) {
		return
	}
	if err := p.Store.SetActiveProfile(ctx, c.Session.ID, c.Account.ID, id); err != nil {
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

// logout ends this browser's session and, with Overfin's own login, the
// login too. It works even before an account exists.
func (p Profiles) logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if c, err := r.Cookie(CookieName); err == nil && len(c.Value) <= 64 {
		if s, err := p.Store.SessionByToken(ctx, c.Value); err == nil {
			_ = p.Store.DeleteSession(ctx, s.AccountID, s.ID)
		}
	}
	clearCookie(w)
	logoutURL := p.LogoutURL
	if a := p.Sessions.Auth; a != nil && a.Login {
		a.Logout(w, r)
		if logoutURL == "" {
			logoutURL = a.Provider.LogoutURL(ctx)
		}
	}
	out := map[string]string{}
	if logoutURL != "" {
		out["logoutUrl"] = logoutURL
	}
	writeOK(w, out)
}
