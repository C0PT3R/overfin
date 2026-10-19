package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"hash/fnv"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/oidc"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
)

const (
	loginPath    = "/api/v1/login"
	gatePath     = "/api/v1/gate"
	stepUpPath   = "/api/v1/step-up"
	callbackPath = "/api/v1/auth/callback"
	finishPath   = "/api/v1/auth/finish"

	// LoginCookie holds a login made through Overfin (identity mode
	// "oidc"); pendingCookie ties a login in progress to the browser that
	// started it.
	LoginCookie   = "__Host-overfin-login"
	pendingCookie = "__Host-overfin-auth"
)

// Auth talks to the identity provider over OpenID Connect. It always offers
// step-up: confirming a fresh login before destructive actions (SECURITY.md
// rule 21). With Login set (identity mode "oidc"), it also logs users in,
// re-checks their logins with the provider, and serves the gate a reverse
// proxy consults before serving the site's pages.
//
// The provider returns the browser to /auth/callback, which only bounces it,
// by a same-origin navigation, to /auth/finish: that hop makes the browser
// send its cookies even when the provider lives on another site.
type Auth struct {
	Sessions    *Sessions
	Store       *store.Store
	Provisioner *provision.Provisioner
	Provider    *oidc.Provider
	// FreshFor is how long a confirmed login counts as fresh for step-up.
	FreshFor time.Duration
	// Login turns on Overfin's own login.
	Login bool
	// LoginLifetime is how long a login lasts without activity.
	LoginLifetime time.Duration
	// Recheck is how often a login is confirmed again with the provider; a
	// user disabled there is out within this time.
	Recheck time.Duration
	// NeedGroups asks the provider for the user's groups, for a required
	// group.
	NeedGroups bool
	Logger     *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingAuth // by state
	locks   [64]sync.Mutex         // re-checks, striped by login ID
}

// pendingAuth is one login or step-up waiting for the provider's answer.
type pendingAuth struct {
	StepUp bool
	// Binding ties the answer to where it started: the session for a
	// step-up, the pending cookie for a login.
	Binding   string
	AccountID string
	Nonce     string
	Verifier  string
	Return    string
	Started   time.Time
}

const (
	// pendingFor is how long a user has to complete a login.
	pendingFor = 10 * time.Minute
	// maxPending caps waiting logins and step-ups; the oldest go first.
	maxPending = 10_000
	// shortLogin is how long a login lasts when the provider gives no
	// refresh token to re-check it with. It doesn't slide with activity.
	shortLogin = 12 * time.Hour
	// outageGrace keeps logins working while the provider is unreachable,
	// counted from the last successful check. After that, they fail closed.
	outageGrace = time.Hour
)

func (*Auth) Name() string { return "auth" }

func (a *Auth) Register(r *plugin.Registrar) error {
	if err := r.HandleFunc("GET "+stepUpPath, a.startStepUp); err != nil {
		return err
	}
	if err := r.HandleNavigation("GET "+callbackPath, http.HandlerFunc(a.callback)); err != nil {
		return err
	}
	if err := r.HandlePublic("GET "+finishPath, http.HandlerFunc(a.finish)); err != nil {
		return err
	}
	if !a.Login {
		return nil
	}
	// Login is reached from the gate's redirect, so possibly from another
	// site; it starts a login for this browser and nothing else.
	if err := r.HandleNavigation("GET "+loginPath, http.HandlerFunc(a.startLogin)); err != nil {
		return err
	}
	return r.HandleCrossSite("GET "+gatePath, http.HandlerFunc(a.gate))
}

// ---- Logins -------------------------------------------------------------------------

// Middleware sets the identity from the login cookie (identity mode
// "oidc"). Identity headers play no part.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(LoginCookie); err == nil && len(c.Value) <= 64 {
			if l := a.current(r.Context(), c.Value); l != nil {
				r = r.WithContext(identity.With(r.Context(), identity.Identity{Subject: l.Subject, Groups: l.Groups}))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// current returns the login for a cookie value, confirming it again with
// the provider when the last check is older than Recheck. It returns nil
// when there is no valid login.
func (a *Auth) current(ctx context.Context, token string) *store.Login {
	l, err := a.Store.LoginByToken(ctx, token)
	if err != nil {
		return nil
	}
	if l.RefreshToken == "" || time.Since(l.Checked) < a.Recheck {
		return l // nothing to re-check with, or checked recently
	}
	// One re-check at a time per login: a page of posters mustn't send
	// twenty refreshes, and a rotated refresh token can only be used once.
	h := fnv.New32a()
	h.Write([]byte(l.ID))
	lock := &a.locks[h.Sum32()%uint32(len(a.locks))]
	lock.Lock()
	defer lock.Unlock()
	if l, err = a.Store.LoginByID(ctx, l.ID); err != nil {
		return nil
	}
	if time.Since(l.Checked) < a.Recheck {
		return l // someone else just did it
	}
	got, err := a.Provider.Refresh(ctx, l.RefreshToken, l.Subject)
	switch {
	case errors.Is(err, oidc.ErrRevoked):
		_ = a.Store.DeleteLogin(ctx, l.ID)
		return nil
	case err != nil:
		if time.Since(l.Checked) < outageGrace {
			a.Logger.Warn("re-checking a login failed; still accepting it for now", "err", err)
			return l
		}
		a.Logger.Error("re-checking a login failed for too long; refusing it", "err", err)
		return nil
	}
	if a.NeedGroups && got.Groups != nil {
		l.Groups = identity.CleanGroups(got.Groups)
	}
	if err := a.Store.LoginChecked(ctx, l.ID, l.Groups, got.RefreshToken, a.LoginLifetime); err != nil {
		a.Logger.Error("saving a login check failed", "err", err)
		return nil
	}
	return l
}

// gate tells a reverse proxy whether to serve a page of the site: yes to
// anyone logged in and allowed, a redirect to the login for a page
// visited by someone who isn't, and no to anything else.
func (a *Auth) gate(w http.ResponseWriter, r *http.Request) {
	if id, ok := identity.FromContext(r.Context()); ok {
		if a.Provisioner.Allowed(id) != nil {
			problem.Send(w, problem.AccountNotAllowed, "")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	dest := r.Header.Get("Sec-Fetch-Dest")
	if dest == "document" || (dest == "" && strings.Contains(r.Header.Get("Accept"), "text/html")) {
		target := loginPath + "?" + url.Values{"return": {safeReturn(r.Header.Get("X-Forwarded-Uri"))}}.Encode()
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	problem.Send(w, problem.NotAuthenticated, "")
}

func (a *Auth) startLogin(w http.ResponseWriter, r *http.Request) {
	ret := safeReturn(r.URL.Query().Get("return"))
	if _, ok := identity.FromContext(r.Context()); ok {
		http.Redirect(w, r, ret, http.StatusSeeOther) // already logged in
		return
	}
	binding := oidc.Random(32)
	scope := []string{"offline_access"}
	if a.NeedGroups {
		scope = append(scope, "profile")
	}
	target, ok := a.begin(w, r, pendingAuth{Binding: binding, Return: ret}, false, scope...)
	if !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: binding, Path: "/", MaxAge: int(pendingFor.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Logout ends this browser's login, if any.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(LoginCookie); err == nil && len(c.Value) <= 64 {
		if l, err := a.Store.LoginByToken(r.Context(), c.Value); err == nil {
			_ = a.Store.DeleteLogin(r.Context(), l.ID)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: LoginCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// ---- Step-up ------------------------------------------------------------------------

func (a *Auth) startStepUp(w http.ResponseWriter, r *http.Request) {
	c := a.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	target, ok := a.begin(w, r, pendingAuth{StepUp: true, Binding: c.Session.ID, AccountID: c.Account.ID,
		Return: safeReturn(r.URL.Query().Get("return"))}, true)
	if ok {
		http.Redirect(w, r, target, http.StatusSeeOther)
	}
}

// ---- Shared -------------------------------------------------------------------------

// safeReturn accepts only a path on this site: no scheme, no other host, no
// "//" or backslash tricks, no control characters.
func safeReturn(s string) string {
	if s == "" || len(s) > 512 || s[0] != '/' || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\") {
		return "/"
	}
	for _, c := range s {
		if c < 0x21 || c == 0x7f {
			return "/"
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return s
}

// begin records a pending login or step-up and returns where to send the
// browser. It writes the problem and returns false on failure.
func (a *Auth) begin(w http.ResponseWriter, r *http.Request, p pendingAuth, fresh bool, scope ...string) (string, bool) {
	p.Nonce, p.Verifier, p.Started = oidc.Random(32), oidc.Random(32), time.Now()
	state := oidc.Random(32)
	target, err := a.Provider.AuthURL(r.Context(), state, p.Nonce, p.Verifier, fresh, scope...)
	if err != nil {
		a.Logger.Error("identity provider unavailable", "err", err)
		problem.Send(w, problem.ServerBusy, "")
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = map[string]pendingAuth{}
	}
	for k, v := range a.pending {
		if p.Started.Sub(v.Started) > pendingFor {
			delete(a.pending, k)
		}
	}
	if len(a.pending) >= maxPending {
		var oldest string
		for k, v := range a.pending {
			if oldest == "" || v.Started.Before(a.pending[oldest].Started) {
				oldest = k
			}
		}
		delete(a.pending, oldest)
	}
	a.pending[state] = p
	return target, true
}

// callback bounces the provider's answer to finish with a same-origin
// navigation. It acts on nothing and needs no cookie.
func (a *Auth) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	next := url.Values{}
	for _, k := range []string{"state", "code", "error"} {
		if v := q.Get(k); v != "" && len(v) <= 2048 {
			next.Set(k, v)
		}
	}
	target := html.EscapeString(finishPath + "?" + next.Encode())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=` +
		target + `"><title>Logging in…</title><a href="` + target + `">Continue</a>`))
}

func (a *Auth) finish(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a.mu.Lock()
	p, ok := a.pending[q.Get("state")]
	delete(a.pending, q.Get("state")) // single use, whatever happens next
	a.mu.Unlock()
	if !ok || time.Since(p.Started) > pendingFor {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if p.StepUp {
		a.finishStepUp(w, r, p)
	} else {
		a.finishLogin(w, r, p)
	}
}

func (a *Auth) finishLogin(w http.ResponseWriter, r *http.Request, p pendingAuth) {
	// The answer must come back to the browser that started the login:
	// otherwise someone could log a victim into the attacker's account.
	c, err := r.Cookie(pendingCookie)
	if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(p.Binding)) != 1 {
		problem.Send(w, problem.NotFound, "")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("code") == "" {
		problem.Send(w, problem.NotAuthenticated, "the login was cancelled")
		return
	}
	ctx := r.Context()
	login, err := a.Provider.Exchange(ctx, q.Get("code"), p.Verifier, p.Nonce)
	if err != nil || !identity.ValidSubject(login.Subject) {
		a.Logger.Warn("login not accepted", "err", err)
		problem.Send(w, problem.NotAuthenticated, "the login could not be confirmed")
		return
	}
	lifetime := a.LoginLifetime
	if login.RefreshToken == "" {
		// Without a refresh token the login can't be re-checked, so it
		// gets a short, fixed life instead.
		lifetime = min(shortLogin, a.LoginLifetime)
		a.Logger.Warn("the identity provider gave no refresh token, so logins last 12 hours " +
			"and aren't re-checked; allow refresh tokens (usually the offline_access scope) to fix that")
	}
	var groups []string
	if a.NeedGroups {
		if login.Groups == nil {
			a.Logger.Error("the identity provider sent no groups claim")
		}
		groups = identity.CleanGroups(login.Groups)
	}
	if a.Provisioner.Allowed(identity.Identity{Subject: login.Subject, Groups: groups}) != nil {
		problem.Send(w, problem.AccountNotAllowed, "")
		return
	}
	_, token, err := a.Store.CreateLogin(ctx, login.Subject, groups, login.RefreshToken, lifetime)
	if err != nil {
		a.Logger.Error("saving a login failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: LoginCookie, Value: token, Path: "/", MaxAge: int(lifetime.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, p.Return, http.StatusSeeOther)
}

func (a *Auth) finishStepUp(w http.ResponseWriter, r *http.Request, p pendingAuth) {
	if _, ok := identity.FromContext(r.Context()); !ok {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	c := a.Sessions.CurrentAccount(w, r)
	if c == nil {
		return
	}
	// The state must come from this very session: a step-up started
	// elsewhere can't be completed here, nor the other way round.
	if p.Binding != c.Session.ID || p.AccountID != c.Account.ID {
		problem.Send(w, problem.NotFound, "")
		return
	}
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("code") == "" {
		// Cancelled at the provider: back to the page, still not fresh.
		http.Redirect(w, r, p.Return, http.StatusSeeOther)
		return
	}
	login, err := a.Provider.Exchange(r.Context(), q.Get("code"), p.Verifier, p.Nonce)
	if err != nil {
		a.Logger.Warn("step-up: login not accepted", "err", err)
		problem.Send(w, problem.StepUpRequired, "the login could not be confirmed")
		return
	}
	// The same person, and a login made after the step-up started: an
	// older session at the provider doesn't count.
	if login.Subject != c.Account.Subject {
		a.Logger.Warn("step-up: login by another user")
		problem.Send(w, problem.StepUpRequired, "that login is for another user")
		return
	}
	if login.AuthTime.Before(p.Started.Add(-5 * time.Second)) {
		problem.Send(w, problem.StepUpRequired, "the identity provider didn't ask for a new login")
		return
	}
	ctx := r.Context()
	if err := a.Store.SetFreshLogin(ctx, c.Account.ID, c.Session.ID, time.Now()); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// The account holder is back: PIN locks lift (rule 22).
	if err := a.Store.UnlockPINs(ctx, c.Account.ID); err != nil {
		a.Logger.Error("step-up: unlocking PINs failed", "err", err)
	}
	http.Redirect(w, r, p.Return, http.StatusSeeOther)
}
