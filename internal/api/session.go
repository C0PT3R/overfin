package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
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
}

// Caller is who is making a request: account, session, active profile.
type Caller struct {
	Account *store.Account
	Session *store.Session
	Profile *store.Profile
	Owner   *store.Profile
}

// IsOwner reports whether the active profile is the account's owner.
func (c *Caller) IsOwner() bool { return c.Profile.ID == c.Owner.ID }

// touchEvery limits how often a session's activity is written.
const touchEvery = time.Minute

// Current resolves the caller, starting a session if needed. It writes the
// problem and returns nil when there is no usable account.
func (s *Sessions) Current(w http.ResponseWriter, r *http.Request) *Caller {
	id, _ := identity.FromContext(r.Context())
	ctx := r.Context()
	acct, owner, err := s.Provisioner.Find(ctx, id)
	switch {
	case err == nil:
	case errors.Is(err, provision.ErrNoAccount):
		problem.Send(w, problem.AccountSetupRequired, "")
		return nil
	case errors.Is(err, provision.ErrNotAllowed):
		problem.Send(w, problem.AccountNotAllowed, "")
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
		created, token, err := s.Store.CreateSession(ctx, acct.ID, owner.ID, deviceName(r.UserAgent()), s.Lifetime)
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

	prof := owner
	if sess.ActiveProfileID != "" && sess.ActiveProfileID != owner.ID {
		if p, err := s.Store.Profile(ctx, acct.ID, sess.ActiveProfileID); err == nil {
			prof = p
		}
	}
	return &Caller{Account: acct, Session: sess, Profile: prof, Owner: owner}
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
