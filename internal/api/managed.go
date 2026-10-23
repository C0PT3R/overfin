package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"math/big"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
)

const (
	signinPath     = "/api/v1/signin"
	signinCodePath = "/api/v1/signin/code"
	signinLinkPath = "/api/v1/signin/link"
	signinJS       = "/api/v1/signin/page.js"
	signinCSS      = "/api/v1/signin/page.css"
	signinLogo     = "/api/v1/signin/logo.svg"

	// bindingCookie ties a code to the browser that asked for it (a base
	// name, as package cookie takes it).
	bindingCookie = "overfin-signin"

	// ManagedAdminGroup is the group managed sign-in gives admins: the
	// provisioner's admin group in managed mode.
	ManagedAdminGroup = "admins"
)

const (
	// codeFor is how long a code sent by email works.
	codeFor = 15 * time.Minute
	// codesPerHour caps the codes sent to one email.
	codesPerHour = 5
	// passwordTries caps wrong passwords for one email per quarter hour.
	passwordTries = 10
	// slideEvery is how often a login's lifetime slides with activity.
	slideEvery = 10 * time.Minute
)

//go:embed signin
var signinFiles embed.FS

var signinPage = template.Must(template.ParseFS(signinFiles, "signin/page.html"))

// Managed is Overfin's own sign-in (identity mode "managed"): people sign
// in with their email and a code Overfin sends there, or with a password if
// they set one. It also confirms fresh logins (step-up) the same way.
//
// Whether an email has an account here is never told: asking for a code
// gets the same answer either way, and a wrong password the same as an
// unknown email. Codes go to unknown emails only when the visitor brings an
// invitation; anyone else is someone an admin added.
type Managed struct {
	Sessions    *Sessions
	Store       *store.Store
	Provisioner *provision.Provisioner
	// Mounts are the switched-on modules, which say whether a visitor
	// brings an invitation.
	Mounts []Mount
	// CodeKey hashes codes, links and bindings (keys.SigninCode).
	CodeKey []byte
	// Mail is the SMTP server codes go through; nil (or a nil result)
	// when there's none, and then people sign in with a password only.
	Mail func() Mailer
	// SiteURL, when set, is the site's public address: emails then carry
	// a link besides the code.
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
	// The page is where emails' links and the site's redirects lead, so
	// possibly from elsewhere; it only shows a form.
	if err := r.HandleNavigation("GET "+signinPath, http.HandlerFunc(m.page)); err != nil {
		return err
	}
	for pattern, h := range map[string]http.HandlerFunc{
		"GET " + signinJS:        m.file("signin/page.js", "text/javascript; charset=utf-8"),
		"GET " + signinCSS:       m.file("signin/page.css", "text/css; charset=utf-8"),
		"GET " + signinLogo:      m.file("signin/logo.svg", "image/svg+xml"),
		"POST " + signinPath:     m.start,
		"POST " + signinCodePath: m.code,
		"POST " + signinLinkPath: m.link,
	} {
		if err := r.HandlePublic(pattern, h); err != nil {
			return err
		}
	}
	if err := r.HandleFunc("GET "+StepUpPath, m.stepUp); err != nil {
		return err
	}
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/me/signin":      m.signinInfo,
		"PUT /api/v1/me/password":    m.setPassword,
		"DELETE /api/v1/me/password": m.removePassword,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return r.HandleCrossSite("GET "+gatePath, http.HandlerFunc(m.gate))
}

// ---- Logins ---------------------------------------------------------------------------

// Middleware sets the identity from the login cookie, for someone who still
// has an account here and isn't disabled.
func (m *Managed) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, u := m.current(r); u != nil {
			id := identity.Identity{Subject: u.Subject}
			if u.Admin {
				id.Groups = []string{ManagedAdminGroup}
			}
			r = r.WithContext(identity.With(r.Context(), id))
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
	if u == nil {
		return "", false, false
	}
	return u.Email, u.Admin, !l.Fresh.IsZero() && time.Since(l.Fresh) < m.FreshFor
}

// Admit lets a request for the site's pages through when the visitor is
// signed in and allowed; a page visit by anyone else goes to the sign-in.
func (m *Managed) Admit(w http.ResponseWriter, r *http.Request) bool {
	return admit(w, r, m.Provisioner, r.URL.RequestURI(), signinPath)
}

// gate answers a reverse proxy asking whether to serve a page of the site.
func (m *Managed) gate(w http.ResponseWriter, r *http.Request) {
	if admit(w, r, m.Provisioner, r.Header.Get("X-Forwarded-Uri"), signinPath) {
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
	http.Redirect(w, r, signinPath+"?"+q.Encode(), http.StatusSeeOther)
}

// ---- One's own sign-in --------------------------------------------------------------------

// signinInfo says how the person signed in signs in: their email, whether
// they have a password, and whether codes can come by email.
func (m *Managed) signinInfo(w http.ResponseWriter, r *http.Request) {
	_, u := m.current(r)
	if u == nil {
		problem.Send(w, problem.NotAuthenticated, "")
		return
	}
	writeOK(w, map[string]any{"email": u.Email, "hasPassword": u.PasswordHash != "", "codes": m.mailer() != nil})
}

// setPassword sets or changes the password, after a fresh login; other
// browsers then sign in again.
func (m *Managed) setPassword(w http.ResponseWriter, r *http.Request) {
	l, u, ok := m.freshUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if !decode(r, &req) {
		m.refuse(w, r, problem.InvalidRequest, "")
		return
	}
	hash, err := password.Hash(req.Password)
	if err != nil {
		m.refuse(w, r, problem.InvalidRequest, "passwordLength")
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

// removePassword removes the password, after a fresh login, when codes can
// come by email instead.
func (m *Managed) removePassword(w http.ResponseWriter, r *http.Request) {
	_, u, ok := m.freshUser(w, r)
	if !ok {
		return
	}
	if m.mailer() == nil {
		m.refuse(w, r, problem.InvalidRequest, "keepPassword")
		return
	}
	if err := m.Store.SetPassword(r.Context(), u.Subject, ""); err != nil {
		m.fail(w, r, err)
		return
	}
	writeOK(w, map[string]bool{"hasPassword": false})
}

// freshUser is the person signed in, if they confirmed it's them in the
// last few minutes; otherwise it answers step-up-required.
func (m *Managed) freshUser(w http.ResponseWriter, r *http.Request) (*store.Login, *store.User, bool) {
	l, u := m.current(r)
	switch {
	case u == nil:
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

// ---- The page -------------------------------------------------------------------------

func (m *Managed) file(name, contentType string) http.HandlerFunc {
	b, err := signinFiles.ReadFile(name)
	if err != nil {
		panic(err) // embedded: present at build time
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	}
}

func (m *Managed) page(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ret := safeReturn(q.Get("return"))
	fresh := q.Get("fresh") == "1"
	_, u := m.current(r)
	switch {
	case !fresh && u != nil:
		http.Redirect(w, r, ret, http.StatusSeeOther) // already signed in
		return
	case fresh && u == nil:
		fresh = false // nobody to confirm: an ordinary sign-in
	}
	m.ensureBinding(w, r)
	lang := pageLang(r)
	data := map[string]any{
		"Lang": lang, "T": signinTexts[lang], "Return": ret, "Fresh": fresh,
		"Codes": m.mailer() != nil, "JS": signinJS, "CSS": signinCSS, "Logo": signinLogo,
	}
	if fresh {
		data["Email"] = u.Email
		data["Intro"] = strings.ReplaceAll(signinTexts[lang]["freshIntro"], "{email}", u.Email)
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; "+
		"form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	if err := signinPage.Execute(w, data); err != nil {
		m.Logger.Error("the sign-in page failed", "err", err)
	}
}

// ensureBinding gives the browser its binding cookie, once.
func (m *Managed) ensureBinding(w http.ResponseWriter, r *http.Request) {
	if c, err := cookie.Get(r, bindingCookie); err == nil && len(c.Value) == 43 {
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: bindingCookie, Value: randomToken(), MaxAge: 30 * 24 * 60 * 60})
}

// binding is the keyed hash of this browser's binding cookie; nil without
// one.
func (m *Managed) binding(r *http.Request) []byte {
	c, err := cookie.Get(r, bindingCookie)
	if err != nil || len(c.Value) != 43 {
		return nil
	}
	return m.hash("binding", c.Value)
}

// ---- Signing in -----------------------------------------------------------------------

type signinRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Code     string `json:"code"`
	Link     string `json:"link"`
	Fresh    bool   `json:"fresh"`
	Return   string `json:"return"`
}

// start takes an email and a password, or an email alone and sends it a
// code.
func (m *Managed) start(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	if req.Password != "" {
		m.withPassword(w, r, req)
		return
	}
	sender := m.mailer()
	if sender == nil {
		m.refuse(w, r, problem.InvalidRequest, "noMail")
		return
	}
	ctx := r.Context()
	// Every request counts, whether or not the email has an account, so
	// the limit tells nothing either.
	n, err := m.Store.CodesSentSince(ctx, req.Email, time.Now().Add(-time.Hour))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if n >= codesPerHour {
		m.refuse(w, r, problem.RateLimited, "tooManyCodes")
		return
	}
	code, link := randomCode(), randomToken()
	purpose := "signin"
	if req.Fresh {
		purpose = "fresh"
	}
	send, err := m.mayGetCode(r, req)
	if err != nil {
		m.fail(w, r, err)
		return
	}
	c := store.SigninCode{Email: req.Email, CodeHash: m.hash("code", req.Email, code), LinkHash: m.hash("link", link),
		Binding: m.binding(r), Purpose: purpose, Expires: time.Now().Add(codeFor)}
	if !send {
		// Counted, but nobody can use it: hashes of nothing anyone has.
		c.CodeHash, c.LinkHash = m.hash("none", randomToken()), m.hash("none", randomToken())
	}
	if c.Binding == nil {
		m.refuse(w, r, problem.InvalidRequest, "reload")
		return
	}
	if err := m.Store.AddSigninCode(ctx, c); err != nil {
		m.fail(w, r, err)
		return
	}
	if send {
		// Sent in the background: how long sending takes would otherwise
		// tell who has an account.
		subject, body := m.codeEmail(pageLang(r), purpose, code, link)
		m.sendings.Add(1)
		go func() {
			defer m.sendings.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := sender.Send(ctx, req.Email, subject, body); err != nil {
				m.Logger.Error("sending a sign-in code failed", "err", err)
			}
		}()
	}
	writeOK(w, map[string]bool{"sent": true})
}

// mayGetCode reports whether the email gets a code: someone with an
// account here (for a fresh login, the signed-in person), or a newcomer
// with an invitation.
func (m *Managed) mayGetCode(r *http.Request, req signinRequest) (bool, error) {
	u, err := m.Store.UserByEmail(r.Context(), req.Email)
	switch {
	case err == nil:
		return !u.Disabled, nil
	case !errors.Is(err, store.ErrNotFound):
		return false, err
	case req.Fresh:
		return false, nil
	}
	return Invited(r, m.Mounts)
}

func (m *Managed) withPassword(w http.ResponseWriter, r *http.Request, req signinRequest) {
	if !m.byEmail.allow(strings.ToLower(req.Email), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	u, err := m.Store.UserByEmail(r.Context(), req.Email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		m.fail(w, r, err)
		return
	}
	// The same work whether or not there's an account, or a password.
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
	m.finish(w, r, u, req)
}

// code takes the code typed in this browser.
func (m *Managed) code(w http.ResponseWriter, r *http.Request) {
	req, ok := m.read(w, r)
	if !ok {
		return
	}
	code := strings.Map(func(c rune) rune {
		if c >= '0' && c <= '9' {
			return c
		}
		return -1
	}, req.Code)
	binding := m.binding(r)
	if len(code) != 6 || binding == nil {
		m.refuse(w, r, problem.SigninFailed, "wrongCode")
		return
	}
	purpose := "signin"
	if req.Fresh {
		purpose = "fresh"
	}
	match, err := m.Store.TakeSigninCode(r.Context(), req.Email, purpose, binding, m.hash("code", req.Email, code))
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if !match {
		m.refuse(w, r, problem.SigninFailed, "wrongCode")
		return
	}
	m.codeTaken(w, r, req)
}

// link takes the link from an email, opened in any browser: whoever has
// the email has it.
func (m *Managed) link(w http.ResponseWriter, r *http.Request) {
	var req signinRequest
	if !m.byIP.allow(identity.ClientIP(r.Context()).String(), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return
	}
	if !decode(r, &req) || len(req.Link) != 43 {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return
	}
	req.Return = safeReturn(req.Return)
	email, purpose, err := m.Store.TakeSigninLink(r.Context(), m.hash("link", req.Link))
	if errors.Is(err, store.ErrNotFound) {
		m.refuse(w, r, problem.SigninFailed, "oldLink")
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	req.Email, req.Fresh = email, purpose == "fresh"
	m.codeTaken(w, r, req)
}

// codeTaken finishes with the person a right code or link is for, making
// a newcomer's sign-in if they bring an invitation.
func (m *Managed) codeTaken(w http.ResponseWriter, r *http.Request, req signinRequest) {
	ctx := r.Context()
	u, err := m.Store.UserByEmail(ctx, req.Email)
	if errors.Is(err, store.ErrNotFound) && !req.Fresh {
		invited, ierr := Invited(r, m.Mounts)
		if ierr != nil {
			m.fail(w, r, ierr)
			return
		}
		if !invited {
			m.refuse(w, r, problem.InvitationRequired, "noInvitation")
			return
		}
		u, err = m.Store.CreateUser(ctx, req.Email, false)
		if errors.Is(err, store.ErrEmailTaken) {
			u, err = m.Store.UserByEmail(ctx, req.Email) // made meanwhile
		}
		if err == nil {
			m.Logger.Info("someone new signed in, with an invitation", "user", m.Provisioner.LogID(u.Subject))
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		m.refuse(w, r, problem.SigninFailed, "wrongCode")
		return
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	if u.Disabled {
		m.refuse(w, r, problem.AccountNotAllowed, "")
		return
	}
	m.finish(w, r, u, req)
}

// finish signs the person in, or, for a fresh login, marks this session
// confirmed.
func (m *Managed) finish(w http.ResponseWriter, r *http.Request, u *store.User, req signinRequest) {
	ctx := r.Context()
	if req.Fresh {
		l, signedIn := m.current(r)
		if signedIn == nil || signedIn.Subject != u.Subject {
			m.refuse(w, r, problem.SigninFailed, "otherPerson")
			return
		}
		if err := m.Store.SetLoginFresh(ctx, l.ID); err != nil {
			m.fail(w, r, err)
			return
		}
		// With an account, its session is confirmed too, and PIN locks
		// lift: the account holder is back (rule 22). An admin may have
		// no account.
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
		writeOK(w, map[string]string{"next": req.Return})
		return
	}
	l, token, err := m.Store.CreateLogin(ctx, u.Subject, nil, "", m.LoginLifetime)
	if err == nil {
		err = m.Store.SetLoginFresh(ctx, l.ID) // just typed: confirmed
	}
	if err != nil {
		m.fail(w, r, err)
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: LoginCookie, Value: token, MaxAge: int(m.LoginLifetime.Seconds())})
	writeOK(w, map[string]string{"next": req.Return})
}

// read decodes a sign-in request, within this browser's rate limit, with a
// usable email (for a fresh login, the signed-in person's).
func (m *Managed) read(w http.ResponseWriter, r *http.Request) (signinRequest, bool) {
	var req signinRequest
	if !m.byIP.allow(identity.ClientIP(r.Context()).String(), time.Now()) {
		m.refuse(w, r, problem.RateLimited, "tooManyTries")
		return req, false
	}
	if !decode(r, &req) || len(req.Password) > password.MaxLength*4 || len(req.Code) > 32 {
		m.refuse(w, r, problem.InvalidRequest, "")
		return req, false
	}
	req.Return = safeReturn(req.Return)
	if req.Fresh {
		_, u := m.current(r)
		if u == nil {
			problem.Send(w, problem.NotAuthenticated, "")
			return req, false
		}
		req.Email = u.Email
		return req, true
	}
	email, ok := cleanEmail(req.Email)
	if !ok {
		m.refuse(w, r, problem.InvalidRequest, "badEmail")
		return req, false
	}
	req.Email = email
	return req, true
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

// Mailer sends one plain-text email (mail.Sender).
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

func (m *Managed) mailer() Mailer {
	if m.Mail == nil {
		return nil
	}
	return m.Mail()
}

// refuse answers with a problem whose detail is the page's text for key,
// in the visitor's language.
func (m *Managed) refuse(w http.ResponseWriter, r *http.Request, t problem.Type, key string) {
	detail := ""
	if key != "" {
		detail = signinTexts[pageLang(r)][key]
	}
	problem.Send(w, t, detail)
}

func (m *Managed) fail(w http.ResponseWriter, r *http.Request, err error) {
	m.Logger.Error("sign-in failed", "err", err)
	m.refuse(w, r, problem.Internal, "")
}

// hash is the keyed hash of a code, a link or a binding, each in its own
// domain.
func (m *Managed) hash(kind string, parts ...string) []byte {
	h := hmac.New(sha256.New, m.CodeKey)
	h.Write([]byte(kind))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(strings.ToLower(p)))
	}
	return h.Sum(nil)
}

// codeEmail is the email carrying a code, and its link when the site's
// address is known.
func (m *Managed) codeEmail(lang, purpose, code, link string) (subject, body string) {
	t := signinTexts[lang]
	subject = fmt.Sprintf(t["mailSubject"], code)
	if purpose == "fresh" {
		subject = fmt.Sprintf(t["mailSubjectFresh"], code)
	}
	body = fmt.Sprintf(t["mailCode"], code) + "\n\n"
	if m.SiteURL != "" {
		body += t["mailLink"] + "\n" + strings.TrimSuffix(m.SiteURL, "/") + signinPath + "#link=" + link + "\n\n"
	}
	body += fmt.Sprintf(t["mailEnd"], int(codeFor.Minutes())) + "\n"
	return subject, body
}

// SigninLink makes a sign-in link for someone an admin hands it to: it
// works once, in any browser, within validFor. It returns its path on the
// site.
func (m *Managed) SigninLink(ctx context.Context, email string, validFor time.Duration) (string, error) {
	link := randomToken()
	c := store.SigninCode{Email: email, CodeHash: m.hash("none", randomToken()), LinkHash: m.hash("link", link),
		Binding: m.hash("none", randomToken()), Purpose: "signin", Expires: time.Now().Add(validFor)}
	if err := m.Store.AddSigninCode(ctx, c); err != nil {
		return "", err
	}
	return signinPath + "#link=" + link, nil
}

// Wait waits for emails being sent; for tests.
func (m *Managed) Wait() { m.sendings.Wait() }

// randomCode is six random digits.
func randomCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", n.Int64())
}

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

// pageLang is the visitor's language among the page's: the first of
// theirs it has.
func pageLang(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		code, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if _, ok := signinTexts[code]; ok {
			return code
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
