package api

import (
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/oidc"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/store"
)

const (
	stepUpPath   = "/api/v1/step-up"
	callbackPath = "/api/v1/step-up/callback"
	finishPath   = "/api/v1/step-up/finish"
)

// StepUp confirms a fresh login with the identity provider before rare,
// destructive actions (SECURITY.md rule 21).
//
// The site sends the browser to /step-up?return=/some/page. Overfin sends it
// on to the provider, asking for a new login. The provider sends it back to
// /step-up/callback, which only bounces it, by a same-origin navigation, to
// /step-up/finish: that hop makes the browser send the session cookie even
// when the provider lives on another site. Finish checks the result against
// the session that started, marks the session fresh, and returns to the page.
type StepUp struct {
	Sessions *Sessions
	Store    *store.Store
	Provider *oidc.Provider
	// FreshFor is how long a confirmed login counts as fresh.
	FreshFor time.Duration
	Logger   *slog.Logger

	mu      sync.Mutex
	pending map[string]pendingStepUp // by state
}

// pendingStepUp is one step-up waiting for the provider's answer.
type pendingStepUp struct {
	SessionID string
	AccountID string
	Nonce     string
	Verifier  string
	Return    string
	Started   time.Time
}

const (
	// pendingFor is how long a user has to complete a login.
	pendingFor = 10 * time.Minute
	// maxPending caps waiting step-ups; the oldest are dropped first.
	maxPending = 10_000
)

func (*StepUp) Name() string { return "step-up" }

func (s *StepUp) Register(r *plugin.Registrar) error {
	if err := r.HandleFunc("GET "+stepUpPath, s.start); err != nil {
		return err
	}
	if err := r.HandleNavigation("GET "+callbackPath, http.HandlerFunc(s.callback)); err != nil {
		return err
	}
	return r.HandleFunc("GET "+finishPath, s.finish)
}

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

func (s *StepUp) start(w http.ResponseWriter, r *http.Request) {
	c := s.Sessions.Current(w, r)
	if c == nil {
		return
	}
	now := time.Now()
	p := pendingStepUp{
		SessionID: c.Session.ID, AccountID: c.Account.ID,
		Nonce: oidc.Random(32), Verifier: oidc.Random(32),
		Return: safeReturn(r.URL.Query().Get("return")), Started: now,
	}
	state := oidc.Random(32)
	target, err := s.Provider.AuthURL(r.Context(), state, p.Nonce, p.Verifier)
	if err != nil {
		s.Logger.Error("step-up: identity provider unavailable", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	s.mu.Lock()
	if s.pending == nil {
		s.pending = map[string]pendingStepUp{}
	}
	for k, v := range s.pending {
		if now.Sub(v.Started) > pendingFor {
			delete(s.pending, k)
		}
	}
	if len(s.pending) >= maxPending {
		var oldest string
		for k, v := range s.pending {
			if oldest == "" || v.Started.Before(s.pending[oldest].Started) {
				oldest = k
			}
		}
		delete(s.pending, oldest)
	}
	s.pending[state] = p
	s.mu.Unlock()
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// callback bounces the provider's answer to finish with a same-origin
// navigation. It acts on nothing and needs no session.
func (s *StepUp) callback(w http.ResponseWriter, r *http.Request) {
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
		target + `"><title>Confirming…</title><a href="` + target + `">Continue</a>`))
}

func (s *StepUp) finish(w http.ResponseWriter, r *http.Request) {
	c := s.Sessions.Current(w, r)
	if c == nil {
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	p, ok := s.pending[q.Get("state")]
	delete(s.pending, q.Get("state")) // single use, whatever happens next
	s.mu.Unlock()
	// The state must come from this very session: a step-up started
	// elsewhere can't be completed here, nor the other way round.
	if !ok || p.SessionID != c.Session.ID || p.AccountID != c.Account.ID || time.Since(p.Started) > pendingFor {
		problem.Send(w, problem.NotFound, "")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		// Cancelled at the provider: back to the page, still not fresh.
		http.Redirect(w, r, p.Return, http.StatusSeeOther)
		return
	}
	login, err := s.Provider.Exchange(r.Context(), q.Get("code"), p.Verifier, p.Nonce)
	if err != nil {
		s.Logger.Warn("step-up: login not accepted", "err", err)
		problem.Send(w, problem.StepUpRequired, "the login could not be confirmed")
		return
	}
	// The same person, and a login made after the step-up started: an
	// older session at the provider doesn't count.
	if login.Subject != c.Account.Subject {
		s.Logger.Warn("step-up: login by another user")
		problem.Send(w, problem.StepUpRequired, "that login is for another user")
		return
	}
	if login.AuthTime.Before(p.Started.Add(-5 * time.Second)) {
		problem.Send(w, problem.StepUpRequired, "the identity provider didn't ask for a new login")
		return
	}
	ctx := r.Context()
	if err := s.Store.SetFreshLogin(ctx, c.Account.ID, c.Session.ID, time.Now()); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	// The account holder is back: PIN locks lift (rule 22).
	if err := s.Store.UnlockPINs(ctx, c.Account.ID); err != nil {
		s.Logger.Error("step-up: unlocking PINs failed", "err", err)
	}
	http.Redirect(w, r, p.Return, http.StatusSeeOther)
}
