// Package admin is Overfin's control panel: a built-in web page and the API
// behind it, served on their own listener (SECURITY.md rule 58).
//
// Nothing here is reachable from the public API: the panel has its own
// listener, its own routes and its own sign-in. It answers only private
// addresses, refuses requests relayed for public ones, and lets in only
// members of the admin group, after a fresh login. A session ends after
// half an hour without activity, and after twelve hours at most.
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"html"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/connector"
	"github.com/C0PT3R/overfin/internal/oidc"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/store"
)

//go:embed ui
var uiFiles embed.FS

// Features is where the panel reads and changes the feature settings.
type Features interface {
	Features() config.Features
	SetFeatures(ctx context.Context, f config.Features) error
}

// Directory is an identity provider connector's view of people, by the
// user ID logins carry.
type Directory interface {
	People(ctx context.Context) (map[string]connector.Person, error)
}

// peopleFor is how long the panel keeps what the directory said, in memory
// only (rule 40).
const peopleFor = 2 * time.Minute

// directory returns the people the connector knows, fetched again when
// what's kept is older than peopleFor.
func (p *Panel) directory(ctx context.Context) (map[string]connector.Person, error) {
	if p.Directory == nil {
		return nil, nil
	}
	p.mu.Lock()
	if p.people != nil && p.clock().Sub(p.peopleAt) < peopleFor {
		people := p.people
		p.mu.Unlock()
		return people, nil
	}
	p.mu.Unlock()
	people, err := p.Directory.People(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.people, p.peopleAt = people, p.clock()
	p.mu.Unlock()
	return people, nil
}

// Panel serves the control panel.
type Panel struct {
	Features    Features
	Store       *store.Store
	Provisioner *provision.Provisioner
	// Directory, when set, tells who the people behind accounts are.
	Directory Directory
	// Provider signs admins in; its redirect URL is the panel's callback and
	// its groups claim is set.
	Provider *oidc.Provider
	// Group is the identity provider group whose members are admins.
	Group  string
	Logger *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session // by token hash
	people   map[string]connector.Person
	peopleAt time.Time
	pending  map[string]pending // by state
	now      func() time.Time
}

// session is a signed-in admin. Kept in memory only: a restart signs
// admins out.
type session struct {
	name     string
	lastSeen time.Time
	expires  time.Time
}

type pending struct {
	binding  string
	nonce    string
	verifier string
	ret      string
	started  time.Time
}

const (
	cookieName    = "__Host-overfin-admin"
	pendingCookie = "__Host-overfin-admin-auth"
	// sessionFor is how long an admin stays signed in at most; idleFor,
	// without any activity.
	sessionFor = 12 * time.Hour
	idleFor    = 30 * time.Minute
	pendingFor = 10 * time.Minute
)

func (p *Panel) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// Handler returns the panel's handler, with its guards.
func (p *Panel) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServerFS(ui)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/login", p.login)
	mux.HandleFunc("GET /admin/auth/callback", p.callback)
	mux.HandleFunc("GET /admin/auth/finish", p.finish)
	mux.HandleFunc("GET /admin/api/session", p.signedIn(p.whoami))
	mux.HandleFunc("GET /admin/api/features", p.signedIn(p.features))
	mux.HandleFunc("PUT /admin/api/features", p.signedIn(p.setFeatures))
	mux.HandleFunc("POST /admin/api/logout", p.logout)
	mux.HandleFunc("GET /admin/api/accounts", p.signedIn(p.accounts))
	mux.HandleFunc("PATCH /admin/api/accounts/{id}", p.signedIn(p.changeAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}", p.signedIn(p.deleteAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}/sessions/{session}", p.signedIn(p.endSession))
	mux.HandleFunc("POST /admin/api/profiles/{id}/unlock-pin", p.signedIn(p.unlockPIN))
	return privateOnly(sameOrigin(headers(mux)))
}

// ---- Guards -----------------------------------------------------------------

// privateOnly refuses clients that aren't on a private address, and requests
// relayed for one that isn't: whatever path the operator chose (LAN, VPN,
// SSH tunnel), the panel never answers the internet.
func privateOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, err := netip.ParseAddrPort(r.RemoteAddr)
		if err != nil || !config.PrivateAddr(peer.Addr()) || !relayedPrivately(r.Header) {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// relayedPrivately reports whether every address a proxy says it relayed
// for is private. Anything unreadable counts as public.
func relayedPrivately(h http.Header) bool {
	var addrs []string
	for _, v := range h.Values("X-Forwarded-For") {
		addrs = append(addrs, strings.Split(v, ",")...)
	}
	addrs = append(addrs, h.Values("X-Real-Ip")...)
	for _, v := range h.Values("Forwarded") {
		for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
			if k, val, ok := strings.Cut(strings.TrimSpace(part), "="); ok && strings.EqualFold(k, "for") {
				val = strings.Trim(val, `"`)
				if strings.HasPrefix(val, "[") {
					val, _, _ = strings.Cut(strings.TrimPrefix(val, "["), "]")
				} else if host, _, ok := strings.Cut(val, ":"); ok && strings.Count(val, ":") == 1 {
					val = host
				}
				addrs = append(addrs, val)
			}
		}
	}
	for _, a := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(a))
		if err != nil || !config.PrivateAddr(ip) {
			return false
		}
	}
	return true
}

// sameOrigin accepts only same-origin requests, except the identity
// provider's return to the callback; changes must be JSON.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			if !(r.Method == http.MethodGet && r.URL.Path == "/admin/auth/callback") {
				http.Error(w, "Cross-site requests are not allowed", http.StatusForbidden)
				return
			}
		}
		// Requests with a body must be JSON, which HTML forms can't send.
		// DELETE has no body, and browsers never send it cross-site
		// without the server's consent.
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "Requests must be JSON", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		next.ServeHTTP(w, r)
	})
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ---- Sign-in ----------------------------------------------------------------

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return string(sum[:])
}

func (p *Panel) current(r *http.Request) *session {
	c, err := r.Cookie(cookieName)
	if err != nil || len(c.Value) > 64 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := hashToken(c.Value)
	s, ok := p.sessions[key]
	now := p.clock()
	if !ok || !now.Before(s.expires) || now.Sub(s.lastSeen) > idleFor {
		delete(p.sessions, key)
		return nil
	}
	s.lastSeen = now
	return s
}

// signedIn lets only signed-in admins through.
func (p *Panel) signedIn(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := p.current(r)
		if s == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"type": "not-authenticated", "loginUrl": "/admin/login"})
			return
		}
		h(w, r, s)
	}
}

// safeReturn accepts only a path inside the panel.
func safeReturn(s string) string {
	if !strings.HasPrefix(s, "/admin/") || strings.HasPrefix(s, "//") || strings.ContainsAny(s, "\\\x00\r\n") || len(s) > 512 {
		return "/admin/"
	}
	return s
}

// login sends the browser to the identity provider for a new login, every
// time: signing in to the panel is always a fresh login.
func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	pd := pending{binding: oidc.Random(32), nonce: oidc.Random(32), verifier: oidc.Random(32),
		ret: safeReturn(r.URL.Query().Get("return")), started: p.clock()}
	state := oidc.Random(32)
	target, err := p.Provider.AuthURL(r.Context(), state, pd.nonce, pd.verifier, true, "profile")
	if err != nil {
		p.Logger.Error("admin sign-in: identity provider unavailable", "err", err)
		http.Error(w, "The identity provider is unavailable", http.StatusServiceUnavailable)
		return
	}
	p.mu.Lock()
	if p.pending == nil {
		p.pending = map[string]pending{}
	}
	for k, v := range p.pending {
		if p.clock().Sub(v.started) > pendingFor {
			delete(p.pending, k)
		}
	}
	if len(p.pending) < 1000 {
		p.pending[state] = pd
	}
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: pd.binding, Path: "/", MaxAge: int(pendingFor.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// callback bounces the provider's answer to finish with a same-origin
// navigation, so the browser sends the pending cookie.
func (p *Panel) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	next := url.Values{}
	for _, k := range []string{"state", "code", "error"} {
		if v := q.Get(k); v != "" && len(v) <= 2048 {
			next.Set(k, v)
		}
	}
	target := html.EscapeString("/admin/auth/finish?" + next.Encode())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=` +
		target + `"><title>Signing in…</title><a href="` + target + `">Continue</a>`))
}

func (p *Panel) finish(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	pd, ok := p.pending[q.Get("state")]
	delete(p.pending, q.Get("state"))
	p.mu.Unlock()
	c, err := r.Cookie(pendingCookie)
	if !ok || err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(pd.binding)) != 1 ||
		p.clock().Sub(pd.started) > pendingFor {
		http.Error(w, "This sign-in has expired; start again", http.StatusNotFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	if q.Get("error") != "" || q.Get("code") == "" {
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
		return
	}
	login, err := p.Provider.Exchange(r.Context(), q.Get("code"), pd.verifier, pd.nonce)
	if err != nil {
		p.Logger.Warn("admin sign-in not accepted", "err", err)
		http.Error(w, "The sign-in could not be confirmed", http.StatusForbidden)
		return
	}
	if !slices.Contains(login.Groups, p.Group) {
		if login.Groups == nil {
			p.Logger.Warn("admin sign-in refused: the login token carries no groups; "+
				"have the identity provider include them (with Authentik: the 'profile' scope mapping)",
				"claim", p.Provider.GroupsClaim)
		} else {
			p.Logger.Warn("admin sign-in refused: not in the admin group", "adminGroup", p.Group, "groups", login.Groups)
		}
		http.Error(w, "This account is not an administrator of this server", http.StatusForbidden)
		return
	}
	if login.AuthTime.Before(pd.started.Add(-5 * time.Second)) {
		p.Logger.Warn("admin sign-in: the identity provider reported an old login",
			"started", pd.started.UTC().Format(time.RFC3339), "loginTime", login.AuthTime.UTC().Format(time.RFC3339))
		http.Error(w, "The identity provider didn't ask for a new login", http.StatusForbidden)
		return
	}
	token := oidc.Random(32)
	now := p.clock()
	p.mu.Lock()
	if p.sessions == nil {
		p.sessions = map[string]*session{}
	}
	for k, s := range p.sessions {
		if !now.Before(s.expires) {
			delete(p.sessions, k)
		}
	}
	p.sessions[hashToken(token)] = &session{name: login.Name, lastSeen: now, expires: now.Add(sessionFor)}
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", MaxAge: int(sessionFor.Seconds()),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	p.Logger.Info("admin signed in")
	http.Redirect(w, r, pd.ret, http.StatusSeeOther)
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		p.mu.Lock()
		delete(p.sessions, hashToken(c.Value))
		p.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// ---- API --------------------------------------------------------------------

func (p *Panel) whoami(w http.ResponseWriter, r *http.Request, s *session) {
	writeJSON(w, http.StatusOK, map[string]any{"name": s.name})
}

// pluginInfo describes a plugin for the panel.
type pluginInfo struct {
	Name        string   `json:"name"`
	Needs       []string `json:"needs,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (p *Panel) features(w http.ResponseWriter, r *http.Request, s *session) {
	var plugins []pluginInfo
	for name, needs := range config.KnownPlugins {
		plugins = append(plugins, pluginInfo{Name: name, Needs: needs, Description: config.PluginDescriptions[name]})
	}
	slices.SortFunc(plugins, func(a, b pluginInfo) int { return strings.Compare(a.Name, b.Name) })
	writeJSON(w, http.StatusOK, map[string]any{
		"features":        p.Features.Features(),
		"defaults":        config.DefaultFeatures(),
		"plugins":         plugins,
		"exposableFields": config.ExposableFields,
	})
}

func (p *Panel) setFeatures(w http.ResponseWriter, r *http.Request, s *session) {
	var f config.Features
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not valid feature settings"})
		return
	}
	if err := p.Features.SetFeatures(r.Context(), f); err != nil {
		// Validation errors name the setting and why; they carry no
		// secrets, and only admins see them.
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	p.Logger.Info("feature settings changed in the control panel")
	writeJSON(w, http.StatusOK, map[string]any{"features": p.Features.Features()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
