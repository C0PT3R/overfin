package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/identity"
	"github.com/C0PT3R/overfin-gateway/internal/problem"
	"github.com/C0PT3R/overfin-gateway/internal/provision"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

// CookieName is Overfin's session cookie. The __Host- prefix makes browsers
// require Secure, Path=/ and no Domain: no other subdomain can set it
// (SECURITY.md rule 17).
const CookieName = "__Host-overfin"

// Sessions ties each browser or device to an account, its active profile and
// its own session. The identity provider says who the visitor is; the
// session remembers which profile this device is using.
type Sessions struct {
	Provisioner *provision.Provisioner
	Store       *store.Store
	Logger      *slog.Logger
	// Lifetime of a session without activity; it slides with use.
	Lifetime time.Duration
	// SetupOffers are the other ways modules offer to set an account up
	// than a new one, as account-setup-required carries them. While there
	// are any, a first visit asks instead of creating an account.
	SetupOffers map[string]string
	// Auth is the instance's OpenID Connect side, nil when it has none:
	// actions that need a fresh login are then refused.
	Auth *Auth
}

// RequireFresh lets an action through only if the session confirmed a login
// recently (rule 21). Otherwise it writes step-up-required, with where to
// confirm one, and returns false.
func (s *Sessions) RequireFresh(w http.ResponseWriter, c *Caller) bool {
	if s.Auth != nil && !c.Session.FreshLogin.IsZero() && time.Since(c.Session.FreshLogin) < s.Auth.FreshFor {
		return true
	}
	p := problem.New(problem.StepUpRequired, "")
	p.StepUp = "login"
	if s.Auth != nil {
		p.StepUpURL = stepUpPath
	} else {
		p.Detail = "this server offers no way to confirm a login, so this can't be done here"
	}
	problem.Write(w, p)
	return false
}

// Caller is who is making a request: account, session, active profile.
// Profile is nil while the session has none chosen (see CurrentAccount).
type Caller struct {
	Account *store.Account
	Session *store.Session
	Profile *store.Profile
	Owner   *store.Profile
}

// IsOwner reports whether the active profile is the account's owner.
func (c *Caller) IsOwner() bool { return c.Profile != nil && c.Profile.ID == c.Owner.ID }

// touchEvery limits how often a session's activity is written.
const touchEvery = time.Minute

// Current resolves the caller, starting a session if needed. It writes the
// problem and returns nil when there is no usable account, or no profile
// chosen yet.
func (s *Sessions) Current(w http.ResponseWriter, r *http.Request) *Caller {
	c := s.CurrentAccount(w, r)
	if c != nil && c.Profile == nil {
		problem.Send(w, problem.ProfileRequired, "")
		return nil
	}
	return c
}

// CurrentAccount is Current for the few routes that work before a profile
// is chosen: the account, the profile list, switching profile, step-up.
//
// A session starts on the owner profile, unless the owner protected it with
// a PIN: then it starts with none, and so does a session whose profile was
// deleted. Falling back to the owner profile would skip its PIN.
func (s *Sessions) CurrentAccount(w http.ResponseWriter, r *http.Request) *Caller {
	id, _ := identity.FromContext(r.Context())
	ctx := r.Context()
	acct, owner, err := s.Provisioner.Find(ctx, id)
	switch {
	case err == nil:
	case errors.Is(err, provision.ErrNoAccount):
		s.setupRequired(w)
		return nil
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
		return nil
	case errors.Is(err, provision.ErrExpired):
		problem.Send(w, problem.AccountExpired, "")
		return nil
	case errors.Is(err, provision.ErrAnonymous):
		problem.Send(w, problem.NotFound, "")
		return nil
	default:
		s.Logger.Error("account lookup failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return nil
	}

	// A session only counts for the account it was created for: a cookie
	// carried to another person's browser is simply ignored there.
	var sess *store.Session
	if c, err := r.Cookie(CookieName); err == nil && len(c.Value) <= 64 {
		if found, err := s.Store.SessionByToken(ctx, c.Value); err == nil && found.AccountID == acct.ID {
			sess = found
		}
	}
	if sess == nil {
		start := owner.ID
		if owner.HasPIN {
			start = ""
		}
		created, token, err := s.Store.CreateSession(ctx, acct.ID, start, deviceName(r.UserAgent()), s.Lifetime)
		if err != nil {
			s.Logger.Error("could not create a session", "err", err)
			problem.Send(w, problem.Internal, "")
			return nil
		}
		sess = created
		s.setCookie(w, token)
	} else if time.Since(sess.LastActive) > touchEvery {
		_ = s.Store.TouchSession(ctx, sess.ID, s.Lifetime)
	}

	var prof *store.Profile
	switch {
	case sess.ActiveProfileID == owner.ID:
		prof = owner
	case sess.ActiveProfileID != "":
		if p, err := s.Store.Profile(ctx, acct.ID, sess.ActiveProfileID); err == nil {
			prof = p
		}
	}
	if prof == nil && !owner.HasPIN {
		prof = owner // nothing to protect: the owner profile is the default
	}
	return &Caller{Account: acct, Session: sess, Profile: prof, Owner: owner}
}

// setupRequired answers that there's no account yet, with the other ways
// modules offer to set one up.
func (s *Sessions) setupRequired(w http.ResponseWriter) {
	p := problem.New(problem.AccountSetupRequired, "")
	p.Offers = s.SetupOffers
	problem.Write(w, p)
}

func (s *Sessions) setCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/",
		MaxAge: int(s.Lifetime.Seconds()), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// deviceName summarizes a User-Agent as "Browser on System". Only this
// summary is stored, never the User-Agent itself (rule 40).
func deviceName(ua string) string {
	browser := ""
	switch {
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"):
		browser = "Opera"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case strings.Contains(ua, "Android"):
		system = "Android"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		system = "iOS"
	case strings.Contains(ua, "Windows"):
		system = "Windows"
	case strings.Contains(ua, "Mac OS X"):
		system = "macOS"
	case strings.Contains(ua, "CrOS"):
		system = "ChromeOS"
	case strings.Contains(ua, "Linux"):
		system = "Linux"
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	return "Unknown device"
}
