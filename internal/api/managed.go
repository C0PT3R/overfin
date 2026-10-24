package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/feature"
	"github.com/overfin/server/internal/identity"
	ofmail "github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/proof"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
)

// SigninPath is the sign-in page: the site sends people there.
const SigninPath = signinPagePath

const (
	// ManagedAdminGroup is the group managed sign-in gives admins: the
	// provisioner's admin group in managed mode.
	ManagedAdminGroup = "admins"

	// passwordTries caps wrong passwords for one email per quarter hour.
	passwordTries = 10
	// slideEvery is how often a login's lifetime slides with activity.
	slideEvery = 10 * time.Minute
	// verifyFor is how long a link confirming an address works; resetFor,
	// a link to choose a new password.
	verifyFor = 24 * time.Hour
	resetFor  = time.Hour
	// UnconfirmedFor is how long someone who registered has to confirm
	// their address before they're removed.
	UnconfirmedFor = 7 * 24 * time.Hour
)

// Managed is Overfin's own sign-in (identity mode "managed"): people sign
// in with their email and their password. Accounts come from an admin, or
// from an invitation (/invite/{code}): the person chooses a password, and
// confirms their address by a link sent there before the account works.
// With emails, a forgotten password gets a link to choose another (/reset);
// without, an admin sets a new one.
//
// Whether an email has an account here is never told: registering, asking
// for a link or getting a wrong password answers the same either way.
type Managed struct {
	Sessions    *Sessions
	Store       *store.Store
	Provisioner *provision.Provisioner
	// Mounts are the switched-on modules: the invitations' is asked about
	// a code.
	Mounts []Mount
	// CodeKey hashes the links sent by email (keys.EmailLink).
	CodeKey []byte
	// Proof checks the pages' forms against scripts.
	Proof *proof.Checker
	// Mail is the SMTP server; nil (or a nil result) when there's none:
	// then nobody registers, and an admin sets forgotten passwords.
	Mail func() Mailer
	// Emails writes the emails.
	Emails *emails.Writer
	// SiteURL is the site's public address, for links in emails: without
	// it, no email with a link can be sent.
	SiteURL string
	// LoginLifetime is how long a login lasts without activity; FreshFor,
	// how long a confirmed login counts as fresh.
	LoginLifetime time.Duration
	FreshFor      time.Duration
	Logger        *slog.Logger

	once     sync.Once
	byIP     *windowLimiter
	byEmail  *windowLimiter
	hashing  chan struct{}
	sendings sync.WaitGroup
}

func (*Managed) Name() string { return "signin" }

func (m *Managed) init() {
	m.once.Do(func() {
		m.byIP = &windowLimiter{max: 20, window: time.Minute}
		m.byEmail = &windowLimiter{max: passwordTries, window: 15 * time.Minute}
		// Password checks are slow on purpose: a few at a time.
		m.hashing = make(chan struct{}, 2)
	})
}

func (m *Managed) Register(r *plugin.Registrar) error {
	m.init()
	// The pages are where emails' links and the site's redirects lead, so
	// possibly from elsewhere; they only show a form.
	if err := r.HandlePage("GET "+signinPagePath, http.HandlerFunc(m.signinPage)); err != nil {
		return err
	}
	if err := r.HandlePage("GET "+resetPagePath, http.HandlerFunc(m.resetPage)); err != nil {
		return err
	}
	for pattern, h := range map[string]http.HandlerFunc{
		"POST /api/v1/signin":          m.signIn,
		"POST /api/v1/signin/password": m.changePassword,
		"POST /api/v1/signin/verify":   m.verify,
		"POST /api/v1/signin/resend":   m.resend,
		"POST /api/v1/register":        m.register,
		"POST /api/v1/reset":           m.askReset,
		"POST /api/v1/reset/password":  m.reset,
	} {
		if err := r.HandlePublic(pattern, h); err != nil {
			return err
		}
	}
	if err := r.HandleFunc("GET "+StepUpPath, m.stepUp); err != nil {
		return err
	}
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/me/signin":   m.signinInfo,
		"PUT /api/v1/me/password": m.setPassword,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return r.HandleCrossSite("GET "+gatePath, http.HandlerFunc(m.gate))
}

// ---- Logins ---------------------------------------------------------------------------

// Middleware sets the identity from the login cookie, for someone who still
// has an account here, isn't disabled, confirmed their address, and has no
// new password to choose first. It also marks them admitted: Overfin let
// them in, so their account needs no invitation of its own.
func (m *Managed) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, u := m.current(r); u != nil && u.Confirmed && !u.MustChange {
			id := identity.Identity{Subject: u.Subject}
			if u.Admin {
				id.Groups = []string{ManagedAdminGroup}
			}
			r = r.WithContext(markAdmitted(identity.With(r.Context(), id)))
		}
		next.ServeHTTP(w, r)
	})
}

// current returns the signed-in person and their login, sliding its
// lifetime now and then; nils when nobody is.
func (m *Managed) current(r *http.Request) (*store.Login, *store.User) {
	c, err := cookie.Get(r, LoginCookie)
	if err != nil || len(c.Value) > 64 {
		return nil, nil
	}
	ctx := r.Context()
	l, err := m.Store.LoginByToken(ctx, c.Value)
	if err != nil {
		return nil, nil
	}
	u, err := m.Store.UserBySubject(ctx, l.Subject)
	if err != nil || u.Disabled {
		return nil, nil
	}
	if time.Since(l.Checked) > slideEvery {
		if err := m.Store.LoginChecked(ctx, l.ID, nil, "", m.LoginLifetime); err != nil {
			m.Logger.Error("sliding a login failed", "err", err)
		}
	}
	return l, u
}

// SiteAdmin reports who is signed in (their email, "" for nobody), whether
// they're an admin, and whether they confirmed it's them in the last few
// minutes: what the control panel on the site's address lets in.
func (m *Managed) SiteAdmin(r *http.Request) (email string, admin, fresh bool) {
	l, u := m.current(r)
	if u == nil || !u.Confirmed || u.MustChange {
		return "", false, false
	}
	// A disabled account is out of the panel too, at once (rule 16).
	if a, err := m.Store.AccountBySubject(r.Context(), u.Subject); err == nil && a.Disabled {
		return u.Email, false, false
	}
	return u.Email, u.Admin, !l.Fresh.IsZero() && time.Since(l.Fresh) < m.FreshFor
}

// Admit lets a request for the site's pages through when the visitor is
// signed in and allowed; a page visit by anyone else goes to the sign-in.
func (m *Managed) Admit(w http.ResponseWriter, r *http.Request) bool {
	return admit(w, r, m.Provisioner, r.URL.RequestURI(), signinPagePath)
}

// gate answers a reverse proxy asking whether to serve a page of the site.
func (m *Managed) gate(w http.ResponseWriter, r *http.Request) {
	if admit(w, r, m.Provisioner, r.Header.Get("X-Forwarded-Uri"), signinPagePath) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// Logout ends this browser's login, if any.
func (m *Managed) Logout(w http.ResponseWriter, r *http.Request) string {
	if c, err := cookie.Get(r, LoginCookie); err == nil && len(c.Value) <= 64 {
		if l, err := m.Store.LoginByToken(r.Context(), c.Value); err == nil {
			_ = m.Store.DeleteLogin(r.Context(), l.ID)
		}
	}
	cookie.Clear(w, r, LoginCookie)
	return ""
}

// stepUp sends someone signed in to confirm it's them.
func (m *Managed) stepUp(w http.ResponseWriter, r *http.Request) {
	q := url.Values{"fresh": {"1"}, "return": {safeReturn(r.URL.Query().Get("return"))}}
	http.Redirect(w, r, signinPagePath+"?"+q.Encode(), http.StatusSeeOther)
}

// ---- The pages ------------------------------------------------------------------------

// signinPage signs someone in; asks the signed-in person to confirm it's
// them (?fresh=1) or to choose a new password first; or, from an email's
// link, confirms an address (#verify=…, read by the page).
func (m *Managed) signinPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := page{Mode: "signin", Return: safeReturn(q.Get("return")), Mail: m.canMail()}
	_, u := m.current(r)
	switch {
	case u != nil && u.Confirmed && u.MustChange:
		p.Mode = "change"
	case u != nil && u.Confirmed && q.Get("fresh") == "1":
		p.Mode, p.Email = "fresh", u.Email
	case u != nil && u.Confirmed:
		http.Redirect(w, r, p.Return, http.StatusSeeOther) // already signed in
		return
	}
	p.Rules = m.rules(r.Context())
	renderPage(w, r, m.Store, m.Logger, p)
}

// resetPage asks for an email to send a link to; from that link
// (#reset=…, read by the page), it takes a new password. Without emails,
// there's no such page: an admin sets a new password.
func (m *Managed) resetPage(w http.ResponseWriter, r *http.Request) {
	if !m.canMail() {
		http.NotFound(w, r)
		return
	}
	renderPage(w, r, m.Store, m.Logger, page{Mode: "reset", Mail: true, Rules: m.rules(r.Context())})
}

// InvitationPage is the registration form an invitation leads to.
func (m *Managed) InvitationPage(w http.ResponseWriter, r *http.Request, valid bool) {
	renderPage(w, r, m.Store, m.Logger, page{Mode: "invite", Valid: valid, Mail: m.canMail(), Rules: m.rules(r.Context())})
}

// ---- Signing in -----------------------------------------------------------------------

type signinRequest struct {
	Email     string `json:"email"`
	Password  string `json:"password"`
	Link      string `json:"link"`
	Fresh     bool   `json:"fresh"`
	Return    string `json:"return"`
	Challenge string `json:"challenge"`
	Answer    string `json:"answer"`
}

// signIn takes an email and a password; or, to confirm it's them, the
// signed-in person's password alone (that one asks no check against
// scripts: its person is signed in).
func (m *Managed) signIn(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	if req.Fresh {
		_, signedIn := m.current(r)
		if signedIn == nil || !signedIn.Confirmed {
			problem.Send(w, problem.NotAuthenticated, "")
			return
		}
		req.Email = signedIn.Email
	} else if !m.checked(w, r, req) || !m.cleanEmail(w, r, &req) {
		return
	}
	if !m.byEmail.allow(strings.ToLower(req.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	u, err := m.Store.UserByEmail(r.Context(), req.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	// The same work whether or not there's an account.
	hash := password.Dummy
	if u != nil && u.PasswordHash != "" {
		hash = u.PasswordHash
	}
	select {
	case m.hashing <- struct{}{}:
	case <-r.Context().Done():
		return
	}
	right := password.Check(hash, req.Password)
	<-m.hashing
	if !right || u == nil || u.PasswordHash == "" || u.Disabled {
		m.refuse(w, r, problem.SigninFailed, "wrongPassword")
		return
	}
	// Only wrong passwords count: signing in often isn't trying.
	m.byEmail.forget(strings.ToLower(req.Email))
	if !u.Confirmed {
		// Only with the right password: it tells nothing to anyone else.
		writeOK(w, map[string]bool{"unconfirmed": true})
		return
	}
	if req.Fresh {
		m.confirmFresh(w, r, u, req.Return)
		return
	}
	m.startLogin(w, r, u, req.Return)
}

// startLogin signs someone in, here; one with a new password to choose is
// sent to choose it first.
func (m *Managed) startLogin(w http.ResponseWriter, r *http.Request, u *store.User, ret string) {
	ctx := r.Context()
	l, token, err := m.Store.CreateLogin(ctx, u.Subject, nil, "", m.LoginLifetime)
	if err == nil {
		err = m.Store.SetLoginFresh(ctx, l.ID) // just typed: confirmed
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: LoginCookie, Value: token, MaxAge: int(m.LoginLifetime.Seconds())})
	if u.MustChange {
		ret = signinPagePath + "?" + url.Values{"return": {ret}}.Encode()
	}
	writeOK(w, map[string]string{"next": ret})
}

// confirmFresh marks the signed-in person's login confirmed: it's them.
func (m *Managed) confirmFresh(w http.ResponseWriter, r *http.Request, u *store.User, ret string) {
	ctx := r.Context()
	l, signedIn := m.current(r)
	if signedIn == nil || signedIn.Subject != u.Subject {
		m.refuse(w, r, problem.SigninFailed, "otherPerson")
		return
	}
	if err := m.Store.SetLoginFresh(ctx, l.ID); err != nil {
		m.fail(w, r, err)
		return
	}
	// With an account, its session is confirmed too, and PIN locks lift:
	// the account holder is back (rule 22). An admin may have no account.
	id, _ := identity.FromContext(ctx)
	if _, _, err := m.Provisioner.Find(ctx, id); err == nil {
		c := m.Sessions.CurrentAccount(w, r)
		if c == nil {
			return
		}
		if err := m.Store.SetFreshLogin(ctx, c.Account.ID, c.Session.ID, time.Now()); err != nil {
			m.fail(w, r, err)
			return
		}
		if err := m.Store.UnlockPINs(ctx, c.Account.ID); err != nil {
			m.Logger.Error("step-up: unlocking PINs failed", "err", err)
		}
	}
	writeOK(w, map[string]string{"next": ret})
}

// changePassword takes the new password of someone signed in who must
// choose one (an admin made their account, or set it for them).
func (m *Managed) changePassword(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	l, u := m.current(r)
	if u == nil || !u.Confirmed {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	if !u.MustChange {
		writeOK(w, map[string]string{"next": req.Return})
		return
	}
	hash, ok := m.newPassword(w, r, req.Password)
	if !ok {
		return
	}
	ctx := r.Context()
	err := m.Store.SetPassword(ctx, u.Subject, hash)
	if err == nil {
		err = m.Store.SetMustChange(ctx, u.Subject, false)
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if err := m.Store.DeleteOtherLogins(ctx, u.Subject, l.ID); err != nil {
		m.Logger.Error("ending other logins after a new password failed", "err", err)
	}
	writeOK(w, map[string]string{"next": req.Return})
}

// ---- Registering ----------------------------------------------------------------------

// register makes the account of someone with an invitation (opened in
// this browser): their email and password, then a link to their email to
// confirm it. An email that has an account here gets a notice instead;
// the answer is the same.
func (m *Managed) register(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok || !m.checked(w, r, req) || !m.cleanEmail(w, r, &req) {
		return
	}
	if !m.canMail() {
		m.refuse(w, r, problem.InvalidRequest, "noMail")
		return
	}
	if !m.byEmail.allow("register "+strings.ToLower(req.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	code, valid, err := m.invitation(r)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if !valid {
		m.refuse(w, r, problem.InvitationRequired, "inviteInvalid")
		return
	}
	hash, ok := m.newPassword(w, r, req.Password)
	if !ok {
		return
	}
	ctx := r.Context()
	lang := pageLang(r, m.Store)
	u, err := m.Store.RegisterUser(ctx, req.Email, hash, code)
	switch {
	case errors.Is(err, store.ErrEmailTaken):
		m.send(req.Email, lang, "exists", strings.TrimSuffix(m.SiteURL, "/")+signinPagePath)
	case err != nil:
		m.fail(w, r, err)
		return
	default:
		m.Logger.Info("someone registered with an invitation", "user", m.Provisioner.LogID(u.Subject))
		m.sendLink(u, "verify", verifyFor, lang)
	}
	writeOK(w, map[string]bool{"sent": true})
}

// invitation is the code of the invitation this browser opened, and
// whether it still works.
func (m *Managed) invitation(r *http.Request) (string, bool, error) {
	c, err := cookie.Get(r, feature.InviteCookie)
	if err != nil {
		return "", false, nil
	}
	inviter, h := inviterOf(m.Mounts)
	if inviter == nil {
		return "", false, nil
	}
	ok, _, err := inviter.Invitation(r.Context(), h, c.Value)
	return c.Value, ok, err
}

// verify takes the link confirming an address: the account works then,
// and its person is signed in, with their invitation to shape it.
func (m *Managed) verify(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	u, ok := m.takeLink(w, r, "verify", req.Link)
	if !ok {
		return
	}
	code, err := m.Store.ConfirmUser(r.Context(), u.Subject)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.keepInvitation(w, r, code)
	u.Confirmed = true
	m.startLogin(w, r, u, "/")
}

// keepInvitation leaves the invitation someone registered with in this
// browser, where their account is about to be set up.
func (m *Managed) keepInvitation(w http.ResponseWriter, r *http.Request, code string) {
	if code != "" {
		cookie.Set(w, r, &http.Cookie{Name: feature.InviteCookie, Value: code, MaxAge: 24 * 60 * 60})
	}
}

// resend sends the link confirming an address again, to someone waiting
// for it; the answer is the same for anyone.
func (m *Managed) resend(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok || !m.checked(w, r, req) || !m.cleanEmail(w, r, &req) {
		return
	}
	if !m.byEmail.allow("resend "+strings.ToLower(req.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	if u, err := m.Store.UserByEmail(r.Context(), req.Email); err == nil && !u.Confirmed && m.canMail() {
		m.sendLink(u, "verify", verifyFor, pageLang(r, m.Store))
	}
	writeOK(w, map[string]bool{"sent": true})
}

// ---- A forgotten password -------------------------------------------------------------

// askReset sends a link to choose a new password, when the email has an
// account here; the answer is the same either way.
func (m *Managed) askReset(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok || !m.checked(w, r, req) || !m.cleanEmail(w, r, &req) {
		return
	}
	if !m.canMail() {
		m.refuse(w, r, problem.InvalidRequest, "noMail")
		return
	}
	if !m.byEmail.allow("reset "+strings.ToLower(req.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	u, err := m.Store.UserByEmail(r.Context(), req.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	if u != nil && !u.Disabled {
		m.sendLink(u, "reset", resetFor, pageLang(r, m.Store))
	}
	writeOK(w, map[string]bool{"sent": true})
}

// reset takes the link to choose a new password, and the password: it
// ends every other login, confirms the address (the link went there), and
// signs the person in.
func (m *Managed) reset(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok || !m.followsRules(w, r, req.Password) {
		return
	}
	// The link first: hashing is slow on purpose, and this page asks no
	// check against scripts.
	u, ok := m.takeLink(w, r, "reset", req.Link)
	if !ok {
		return
	}
	hash, ok := m.hashPassword(w, r, req.Password)
	if !ok {
		return
	}
	ctx := r.Context()
	err := m.Store.SetPassword(ctx, u.Subject, hash)
	if err == nil {
		err = m.Store.SetMustChange(ctx, u.Subject, false)
	}
	var code string
	if err == nil && !u.Confirmed {
		code, err = m.Store.ConfirmUser(ctx, u.Subject)
	}
	if err == nil {
		err = m.Store.DeleteLogins(ctx, u.Subject)
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	m.keepInvitation(w, r, code)
	u.Confirmed, u.MustChange = true, false
	m.startLogin(w, r, u, "/")
}

// ---- One's own password ---------------------------------------------------------------

// signinInfo says how the person signed in signs in: their email, and
// whether a forgotten password can be reset by email.
func (m *Managed) signinInfo(w http.ResponseWriter, r *http.Request) {
	_, u := m.current(r)
	if u == nil {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	if !m.ownerHere(w, r) {
		return
	}
	writeOK(w, map[string]any{"email": u.Email, "hasPassword": u.PasswordHash != "", "reset": m.canMail()})
}

// setPassword changes the password, after a fresh login; other browsers
// then sign in again.
func (m *Managed) setPassword(w http.ResponseWriter, r *http.Request) {
	l, u, ok := m.freshUser(w, r)
	if !ok || !m.ownerHere(w, r) {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decode(r, &req) {
		m.refuse(w, r, problem.InvalidRequest, "")
		return
	}
	hash, ok := m.newPassword(w, r, req.Password)
	if !ok {
		return
	}
	if err := m.Store.SetPassword(r.Context(), u.Subject, hash); err != nil {
		m.fail(w, r, err)
		return
	}
	if err := m.Store.DeleteOtherLogins(r.Context(), u.Subject, l.ID); err != nil {
		m.Logger.Error("ending other logins after a new password failed", "err", err)
	}
	writeOK(w, map[string]bool{"hasPassword": true})
}

// SiteOwner reports whether the person signed in uses, in this browser,
// their account's owner profile (or has no account yet, or no session
// here): only then does their login open the control panel on the site.
// Whoever uses another profile here shares that login, fresh for a few
// minutes after each sign-in (rule 25).
func (m *Managed) SiteOwner(r *http.Request) bool {
	_, u := m.current(r)
	if u == nil {
		return false
	}
	ctx := r.Context()
	acct, owner, err := m.Provisioner.Find(ctx, identity.Identity{Subject: u.Subject})
	if errors.Is(err, provision.ErrNoAccount) {
		return true
	}
	if err != nil {
		return false
	}
	if c, err := cookie.Get(r, CookieName); err == nil && len(c.Value) <= 64 {
		if sess, err := m.Store.SessionByToken(ctx, c.Value); err == nil && sess.AccountID == acct.ID {
			return sess.ActiveProfileID == owner.ID || (sess.ActiveProfileID == "" && !owner.HasPIN)
		}
	}
	// No session in this browser: nobody uses another profile here (an
	// admin on the control panel's own address, say).
	return true
}

// ownerHere says whether this browser uses the account owner's profile,
// answering otherwise: the sign-in is the whole account's (SECURITY.md
// rule 25), and whoever uses another profile here shares its login, fresh
// for a few minutes after each sign-in. Before there's an account, there's
// no other profile.
func (m *Managed) ownerHere(w http.ResponseWriter, r *http.Request) bool {
	id, _ := identity.FromContext(r.Context())
	if _, _, err := m.Provisioner.Find(r.Context(), id); errors.Is(err, provision.ErrNoAccount) {
		return true
	}
	c := m.Sessions.Current(w, r)
	if c == nil {
		return false
	}
	if !c.IsOwner() {
		problem.Send(w, problem.OwnerOnly, "")
		return false
	}
	return true
}

// freshUser is the person signed in, if they confirmed it's them in the
// last few minutes; otherwise it answers step-up-required.
func (m *Managed) freshUser(w http.ResponseWriter, r *http.Request) (*store.Login, *store.User, bool) {
	l, u := m.current(r)
	switch {
	case u == nil || !u.Confirmed:
		problem.Send(w, problem.NotAuthenticated, "")
		return nil, nil, false
	case l.Fresh.IsZero() || time.Since(l.Fresh) >= m.FreshFor:
		p := problem.New(problem.StepUpRequired, "")
		p.StepUp, p.StepUpURL = "login", StepUpPath
		problem.Write(w, p)
		return nil, nil, false
	}
	return l, u, true
}

// ---- Helpers --------------------------------------------------------------------------

// read decodes a request from one of the pages, within this browser's
// rate limit.
func (m *Managed) read(w http.ResponseWriter, r *http.Request) (signinRequest, bool) {
	var req signinRequest
	if !m.byIP.allow(identity.ClientIP(r.Context()).String(), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return req, false
	}
	if !decode(r, &req) || len(req.Password) > password.MaxLength*4 || len(req.Link) > 64 {
		m.refuse(w, r, problem.InvalidRequest, "")
		return req, false
	}
	req.Return = safeReturn(req.Return)
	return req, true
}

// checked says whether a form anyone can send came with its check against
// scripts solved, answering when it didn't.
func (m *Managed) checked(w http.ResponseWriter, r *http.Request, req signinRequest) bool {
	if m.Proof == nil || !m.Proof.Check(req.Challenge, req.Answer) {
		m.refuse(w, r, problem.InvalidRequest, "proof")
		return false
	}
	return true
}

// cleanEmail checks the request's email, answering when it isn't one.
func (m *Managed) cleanEmail(w http.ResponseWriter, r *http.Request, req *signinRequest) bool {
	email, ok := cleanEmail(req.Email)
	if !ok {
		m.refuse(w, r, problem.InvalidRequest, "badEmail")
		return false
	}
	req.Email = email
	return true
}

// cleanEmail accepts one plain address, which it returns trimmed.
func cleanEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 || !utf8.ValidString(s) {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || !strings.Contains(s[strings.LastIndexByte(s, '@'):], ".") {
		return "", false
	}
	return s, true
}

// rules are the password rules now.
func (m *Managed) rules(ctx context.Context) password.Rules {
	r, err := m.Store.PasswordRules(ctx)
	if err != nil {
		m.Logger.Error("reading the password rules failed", "err", err)
	}
	return r
}

// newPassword checks a new password against the rules and hashes it,
// answering what it lacks.
func (m *Managed) newPassword(w http.ResponseWriter, r *http.Request, pw string) (string, bool) {
	if !m.followsRules(w, r, pw) {
		return "", false
	}
	return m.hashPassword(w, r, pw)
}

// followsRules says whether a new password follows the rules, answering
// when it doesn't.
func (m *Managed) followsRules(w http.ResponseWriter, r *http.Request, pw string) bool {
	rules := m.rules(r.Context())
	if lacks := rules.Lacks(pw); lacks != "" {
		key := map[string]string{"length": "lacksLength", "digit": "lacksDigit", "special": "lacksSpecial"}[lacks]
		detail := strings.ReplaceAll(signinTexts[pageLang(r, m.Store)][key], "{n}", strconv.Itoa(rules.MinLength))
		problem.Send(w, problem.InvalidRequest, detail)
		return false
	}
	return true
}

// hashPassword hashes a new password, a few at a time (it's slow on
// purpose).
func (m *Managed) hashPassword(w http.ResponseWriter, r *http.Request, pw string) (string, bool) {
	select {
	case m.hashing <- struct{}{}:
	case <-r.Context().Done():
		return "", false
	}
	hash, err := password.Hash(pw)
	<-m.hashing
	if err != nil {
		m.fail(w, r, err)
		return "", false
	}
	return hash, true
}

// takeLink uses up a link from an email, answering when it doesn't work.
func (m *Managed) takeLink(w http.ResponseWriter, r *http.Request, purpose, link string) (*store.User, bool) {
	if len(link) != 43 {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return nil, false
	}
	subject, err := m.Store.TakeEmailLink(r.Context(), purpose, m.hash(purpose, link))
	if errors.Is(err, store.ErrNotFound) {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return nil, false
	}
	if err != nil {
		m.fail(w, r, err)
		return nil, false
	}
	u, err := m.Store.UserBySubject(r.Context(), subject)
	if err != nil || u.Disabled {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return nil, false
	}
	return u, true
}

// sendLink sends someone a link: to confirm their address ("verify", to
// /signin) or to choose a new password ("reset", to /reset). The link is
// saved in the background too, with the email: saving it takes time only
// when there's someone to send it to (rule 66).
func (m *Managed) sendLink(u *store.User, purpose string, validFor time.Duration, lang string) {
	token := randomToken()
	where := signinPagePath + "#verify="
	if purpose == "reset" {
		where = resetPagePath + "#reset="
	}
	m.sendAfter(u.Email, lang, purpose, strings.TrimSuffix(m.SiteURL, "/")+where+token, func(ctx context.Context) error {
		return m.Store.AddEmailLink(ctx, store.EmailLink{Subject: u.Subject, LinkHash: m.hash(purpose, token),
			Purpose: purpose, Expires: time.Now().Add(validFor)})
	})
}

// send sends an email in the background: how long sending takes would
// otherwise tell who has an account. It's in the account's language when
// the person has one, else the page's.
func (m *Managed) send(to, lang, name, link string) { m.sendAfter(to, lang, name, link, nil) }

// sendAfter is send, once first (when not nil) worked.
func (m *Managed) sendAfter(to, lang, name, link string, first func(context.Context) error) {
	sender := m.mailer()
	if sender == nil {
		return
	}
	m.sendings.Add(1)
	go func() {
		defer m.sendings.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if first != nil {
			if err := first(ctx); err != nil {
				m.Logger.Error("saving an email's link failed", "email", name, "err", err)
				return
			}
		}
		msg, err := m.Emails.Write(ctx, name, m.mailLang(ctx, to, lang), emails.Vars{Link: link})
		if err == nil {
			err = sender.SendMessage(ctx, to, msg)
		}
		if err != nil {
			m.Logger.Error("sending an email failed", "email", name, "err", err)
		}
	}()
}

// Mailer sends one email (mail.Sender).
type Mailer interface {
	SendMessage(ctx context.Context, to string, m ofmail.Message) error
}

func (m *Managed) mailer() Mailer {
	if m.Mail == nil {
		return nil
	}
	return m.Mail()
}

// canMail says whether emails with links can be sent: an SMTP server, and
// the site's address for the links.
func (m *Managed) canMail() bool { return m.mailer() != nil && m.SiteURL != "" }

// CanMail is canMail, for the control panel.
func (m *Managed) CanMail() bool { return m.canMail() }

// refuse answers with a problem whose detail is the page's text for key,
// in the visitor's language.
func (m *Managed) refuse(w http.ResponseWriter, r *http.Request, t problem.Type, key string) {
	detail := ""
	if key != "" {
		detail = signinTexts[pageLang(r, m.Store)][key]
	}
	problem.Send(w, t, detail)
}

func (m *Managed) fail(w http.ResponseWriter, r *http.Request, err error) {
	m.Logger.Error("sign-in failed", "err", err)
	m.refuse(w, r, problem.Internal, "failed")
}

// hash is the keyed hash of a link, in its purpose's own domain.
func (m *Managed) hash(kind string, parts ...string) []byte {
	h := hmac.New(sha256.New, m.CodeKey)
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	return h.Sum(nil)
}

// Wait waits for emails being sent; for tests.
func (m *Managed) Wait() { m.sendings.Wait() }

// randomToken is 256 random bits, as 43 URL-safe characters.
func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---- Shared with Auth -----------------------------------------------------------------

// admit lets a request for the site's pages through when the visitor has
// an identity and is allowed. Otherwise it answers itself, sending a page
// visit to signIn and back, and reports false.
func admit(w http.ResponseWriter, r *http.Request, prov *provision.Provisioner, ret, signIn string) bool {
	if id, ok := identity.FromContext(r.Context()); ok {
		if prov.Allowed(id) != nil {
			problem.Send(w, problem.AccountNotAllowed, "")
			return false
		}
		return true
	}
	dest := r.Header.Get("Sec-Fetch-Dest")
	if dest == "document" || (dest == "" && strings.Contains(r.Header.Get("Accept"), "text/html")) {
		target := signIn + "?" + url.Values{"return": {safeReturn(ret)}}.Encode()
		http.Redirect(w, r, target, http.StatusFound)
		return false
	}
	problem.Send(w, problem.NotAuthenticated, "")
	return false
}

// mailLang is the language of an email to someone: their account's when
// they have one, else the browser's.
func (m *Managed) mailLang(ctx context.Context, email, browser string) string {
	u, err := m.Store.UserByEmail(ctx, email)
	if err != nil {
		return browser
	}
	acct, err := m.Store.AccountBySubject(ctx, u.Subject)
	if err != nil {
		return browser
	}
	tag, err := m.Store.LanguageOf(ctx, acct)
	if err != nil || tag == "" {
		return browser
	}
	return tag
}

// pageLang is the visitor's language among the pages': the first of
// theirs they have, else the server's default language (st's, when the
// pages have it), else English.
func pageLang(r *http.Request, st *store.Store) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		code, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if _, ok := signinTexts[code]; ok {
			return code
		}
	}
	if st != nil {
		if _, def, _, err := st.Languages(r.Context()); err == nil {
			code, _, _ := strings.Cut(strings.ToLower(def), "-")
			if _, ok := signinTexts[code]; ok {
				return code
			}
		}
	}
	return "en"
}

// windowLimiter allows each key max events per window.
type windowLimiter struct {
	max    int
	window time.Duration
	mu     sync.Mutex
	recent map[string][]time.Time
}

// forget clears key's tries.
func (l *windowLimiter) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.recent, key)
}

func (l *windowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.recent == nil {
		l.recent = map[string][]time.Time{}
	}
	kept := l.recent[key][:0]
	for _, t := range l.recent[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.max {
		l.recent[key] = kept
		return false
	}
	l.recent[key] = append(kept, now)
	// Forget quiet keys now and then.
	if len(l.recent) > 10_000 {
		for k, times := range l.recent {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= l.window {
				delete(l.recent, k)
			}
		}
	}
	return true
}
