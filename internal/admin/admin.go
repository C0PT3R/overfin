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

	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/introskipper"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/theme"
	"github.com/overfin/server/internal/tmdb"
)

//go:embed ui
var uiFiles embed.FS

// Features is where the panel reads and changes the feature settings.
type Features interface {
	Features() config.Features
	SetFeatures(ctx context.Context, f config.Features) error
}

// TMDBTokens is where the panel reads and changes the operator's TMDB token.
type TMDBTokens interface {
	// TMDBToken says whether there's one, and whether the environment sets
	// it (then the panel can't change it).
	TMDBToken() (set, inEnv bool)
	// SetTMDBToken tests, saves and applies a token; "" removes it. Its
	// error says what's wrong in plain words.
	SetTMDBToken(ctx context.Context, token string) (string, error)
}

// Jellyfin is where the panel reads and changes how Overfin reaches
// Jellyfin.
type Jellyfin interface {
	// Jellyfin is the address in effect, and whether the configuration
	// file sets it (then the panel can't change it).
	Jellyfin() (url string, inFile bool)
	// SetJellyfin tests and applies a new address for the same server.
	// Its error says what's wrong in plain words.
	SetJellyfin(ctx context.Context, url string) (string, error)
	// JellyfinAccess tells whether access to Jellyfin itself is open, and
	// since when.
	JellyfinAccess(ctx context.Context) (open bool, since time.Time, err error)
	// SetJellyfinAccess opens access, returning the account and its new
	// password (shown once), or closes it.
	SetJellyfinAccess(ctx context.Context, open bool) (name, password string, err error)
}

// Seerr is where the panel reads and changes Seerr's connection.
type Seerr interface {
	// Seerr is its address; empty when it isn't connected.
	Seerr() string
	// SetSeerr tests and applies an address and key (empty key: keep it);
	// both empty disconnects it. Its error says what's wrong in plain words.
	SetSeerr(ctx context.Context, url, key string) (string, error)
}

// Catalog says where the catalog's index stands.
type Catalog interface {
	Status(ctx context.Context) (catalog.Status, error)
}

// Tab is a switched-on module, as the control panel shows it.
type Tab struct {
	Name        string
	Description string
	// UI is the module's tab (panel.js and what it needs); nil if none.
	UI fs.FS
}

// Modules is where the panel finds the switched-on modules: their tabs and
// their control panel routes. It changes as modules are switched.
type Modules interface {
	Tabs() []Tab
	AdminRoutes() http.Handler
}

// Panel serves the control panel.
type Panel struct {
	Features Features
	// Jellyfin, when set, shows and changes how Overfin reaches Jellyfin.
	Jellyfin Jellyfin
	// Libraries, when set, manages the libraries of Overfin's Jellyfin.
	Libraries Libraries
	// Transcoding, when set, chooses the GPU Overfin's Jellyfin uses.
	Transcoding Transcoding
	// Catalog, when set, shows where the catalog's index stands.
	Catalog Catalog
	// Seerr, when set, shows and changes Seerr's connection.
	Seerr Seerr
	// TMDB, when set, says whether Overfin has a source for TMDB's data
	// (TMDB itself, or Seerr).
	TMDB func() bool
	// TMDBTokens, when set, shows and changes the operator's TMDB token.
	TMDBTokens TMDBTokens
	// IntroSkipper, when set, shows Intro Skipper's state on Overfin's
	// Jellyfin, and installs it.
	IntroSkipper *introskipper.Installer
	// OnLanguage, when set, runs after TMDB's language changed.
	OnLanguage func()
	// SearchTMDB, when set, searches TMDB for films and series
	// (Identification); Images serves their posters; OnRetitled runs after
	// a title was identified by hand.
	SearchTMDB  func(ctx context.Context, query string) ([]tmdb.Title, error)
	Images      *images.Pipeline
	OnRetitled  func()
	Store       *store.Store
	Provisioner *provision.Provisioner
	// Directory, when set, tells who the people behind accounts are. It
	// keeps what it read briefly (connector.Cache).
	Directory connector.Directory
	// Modules, when set, lists the switched-on modules for their tabs.
	Modules Modules
	// Provider signs admins in; its redirect URL is the panel's callback and
	// its groups claim is set.
	Provider *oidc.Provider
	// SiteAdmin, with managed sign-in, says who's signed in on the site
	// (their email; "" for nobody), whether they're an admin, and whether
	// they confirmed it's them in the last few minutes. Admins then sign in
	// to the panel through the site's own sign-in, not Provider.
	SiteAdmin func(r *http.Request) (email string, admin, fresh bool)
	// People, with managed sign-in, makes sign-in links; the People view
	// lists who may sign in.
	People People
	// Mail, with managed sign-in, shows and changes the SMTP server.
	Mail Mail
	// Group is the identity provider group whose members are admins.
	Group  string
	Logger *slog.Logger

	mu       sync.Mutex
	sessions map[string]*session // by token hash
	pending  map[string]pending  // by state
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
	// Base names, as package cookie takes them.
	cookieName    = "overfin-admin"
	pendingCookie = "overfin-admin-auth"
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

// Handler returns the panel's handler, with its guards, for its own
// listener: private addresses only.
func (p *Panel) Handler() http.Handler { return privateOnly(p.handler()) }

// SiteHandler returns the panel's handler for /admin/ (and /apps/) on the
// site's own address; with private, it answers private addresses only.
func (p *Panel) SiteHandler(private bool) http.Handler {
	if private {
		return privateOnly(p.handler())
	}
	return p.handler()
}

func (p *Panel) handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(uiFiles, "ui")
	mux.Handle("GET /admin/", http.StripPrefix("/admin/", http.FileServerFS(ui)))
	mux.Handle("GET /admin/theme.css", theme.Handler())
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	mux.HandleFunc("GET /admin/login", p.login)
	mux.HandleFunc("GET /admin/auth/callback", p.callback)
	mux.HandleFunc("GET /admin/auth/finish", p.finish)
	mux.HandleFunc("GET /admin/api/session", p.signedIn(p.whoami))
	mux.HandleFunc("GET /admin/api/features", p.signedIn(p.features))
	mux.HandleFunc("PUT /admin/api/features", p.signedIn(p.setFeatures))
	mux.HandleFunc("GET /admin/api/jellyfin", p.signedIn(p.jellyfin))
	mux.HandleFunc("GET /admin/api/catalog", p.signedIn(p.catalog))
	mux.HandleFunc("GET /admin/api/seerr", p.signedIn(p.seerr))
	mux.HandleFunc("PUT /admin/api/seerr", p.signedIn(p.setSeerr))
	mux.HandleFunc("GET /admin/api/ratings", p.signedIn(p.ratings))
	mux.HandleFunc("PUT /admin/api/ratings", p.signedIn(p.setRatings))
	mux.HandleFunc("PUT /admin/api/jellyfin", p.signedIn(p.setJellyfin))
	mux.HandleFunc("PUT /admin/api/jellyfin/access", p.signedIn(p.setJellyfinAccess))
	mux.HandleFunc("PUT /admin/api/tmdb-token", p.signedIn(p.setTMDBToken))
	mux.HandleFunc("GET /admin/api/intro-skipper", p.signedIn(p.introSkipper))
	mux.HandleFunc("POST /admin/api/intro-skipper", p.signedIn(p.installIntroSkipper))
	mux.HandleFunc("DELETE /admin/api/intro-skipper", p.signedIn(p.removeIntroSkipper))
	mux.HandleFunc("GET /admin/api/languages", p.signedIn(p.languages))
	mux.HandleFunc("PUT /admin/api/languages", p.signedIn(p.setLanguages))
	mux.HandleFunc("GET /admin/api/transcoding", p.signedIn(p.transcoding))
	mux.HandleFunc("POST /admin/api/transcoding/test", p.signedIn(p.testTranscoding))
	mux.HandleFunc("GET /admin/api/transcoding/test", p.signedIn(p.transcodingTest))
	mux.HandleFunc("PUT /admin/api/transcoding", p.signedIn(p.setTranscoding))
	mux.HandleFunc("GET /admin/api/libraries", p.signedIn(p.libraries))
	mux.HandleFunc("POST /admin/api/libraries", p.signedIn(p.addLibrary))
	mux.HandleFunc("PATCH /admin/api/libraries/{name}", p.signedIn(p.changeLibrary))
	mux.HandleFunc("DELETE /admin/api/libraries/{name}", p.signedIn(p.removeLibrary))
	mux.HandleFunc("POST /admin/api/libraries/scan", p.signedIn(p.scanLibraries))
	mux.HandleFunc("GET /admin/api/folders", p.signedIn(p.browseFolders))
	mux.HandleFunc("POST /admin/api/logout", p.logout)
	mux.HandleFunc("GET /admin/api/accounts", p.signedIn(p.accounts))
	mux.HandleFunc("PATCH /admin/api/accounts/{id}", p.signedIn(p.changeAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}", p.signedIn(p.deleteAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}/sessions/{session}", p.signedIn(p.endSession))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}/history-copy", p.signedIn(p.allowHistoryCopy))
	mux.HandleFunc("POST /admin/api/profiles/{id}/unlock-pin", p.signedIn(p.unlockPIN))
	mux.HandleFunc("GET /admin/api/apps", p.signedIn(p.appKeys))
	mux.HandleFunc("POST /admin/api/apps", p.signedIn(p.createAppKey))
	mux.HandleFunc("DELETE /admin/api/apps/{id}", p.signedIn(p.deleteAppKey))
	// What apps read with their keys (no admin sign-in: the key says who).
	mux.HandleFunc("GET /apps/v1/watch-stats", p.watchStats)
	mux.HandleFunc("GET /admin/api/identification", p.signedIn(p.identification))
	mux.HandleFunc("POST /admin/api/identification/search", p.signedIn(p.identificationSearch))
	mux.HandleFunc("PUT /admin/api/identification/{id}", p.signedIn(p.identify))
	mux.HandleFunc("DELETE /admin/api/identification/{id}", p.signedIn(p.unidentify))
	mux.HandleFunc("GET /admin/api/posters/{id}", p.signedIn(p.poster))
	mux.HandleFunc("GET /admin/api/modules", p.signedIn(p.moduleTabs))
	if p.People != nil {
		mux.HandleFunc("GET /admin/api/people", p.signedIn(p.people))
		mux.HandleFunc("POST /admin/api/people", p.signedIn(p.addPerson))
		mux.HandleFunc("PATCH /admin/api/people/{id}", p.signedIn(p.changePerson))
		mux.HandleFunc("POST /admin/api/people/{id}/link", p.signedIn(p.personLink))
		mux.HandleFunc("DELETE /admin/api/people/{id}", p.signedIn(p.removePerson))
	}
	if p.Mail != nil {
		mux.HandleFunc("GET /admin/api/mail", p.signedIn(p.mail))
		mux.HandleFunc("PUT /admin/api/mail", p.signedIn(p.setMail))
	}
	// Modules' own control panel routes, as currently switched on.
	modulesAPI := p.signedIn(func(w http.ResponseWriter, r *http.Request, _ *session) {
		if p.Modules == nil {
			http.NotFound(w, r)
			return
		}
		p.Modules.AdminRoutes().ServeHTTP(w, r)
	})
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.Handle(method+" /admin/api/modules/", modulesAPI)
	}
	mux.HandleFunc("GET /admin/modules/{name}/{file...}", p.signedIn(p.moduleFile))
	mux.HandleFunc("GET /admin/api/templates", p.signedIn(p.templates))
	mux.HandleFunc("POST /admin/api/templates", p.signedIn(p.createTemplate))
	mux.HandleFunc("PUT /admin/api/templates/{id}", p.signedIn(p.updateTemplate))
	mux.HandleFunc("DELETE /admin/api/templates/{id}", p.signedIn(p.deleteTemplate))
	return sameOrigin(headers(mux))
}

// ---- Guards -----------------------------------------------------------------

// PrivateOnly is privateOnly, for a listener serving more than the panel.
func PrivateOnly(next http.Handler) http.Handler { return privateOnly(next) }

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
	c, err := cookie.Get(r, cookieName)
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
		if s != nil && p.SiteAdmin != nil {
			// Still the same admin on the site: signing out there, or
			// losing the admin flag, ends the panel's session too.
			if email, isAdmin, _ := p.SiteAdmin(r); email != s.name || !isAdmin {
				c, _ := cookie.Get(r, cookieName)
				p.mu.Lock()
				delete(p.sessions, hashToken(c.Value))
				p.mu.Unlock()
				s = nil
			}
		}
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
	if p.SiteAdmin != nil {
		p.siteLogin(w, r)
		return
	}
	pd := pending{binding: oidc.Random(32), nonce: oidc.Random(32), verifier: oidc.Random(32),
		ret: safeReturn(r.URL.Query().Get("return")), started: p.clock()}
	state := oidc.Random(32)
	// Groups come with "profile" (Authentik) or "groups" (Pocket ID).
	target, err := p.Provider.AuthURL(r.Context(), state, pd.nonce, pd.verifier, true, "profile", "groups")
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
	cookie.Set(w, r, &http.Cookie{Name: pendingCookie, Value: pd.binding, MaxAge: int(pendingFor.Seconds())})
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
	c, err := cookie.Get(r, pendingCookie)
	if !ok || err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(pd.binding)) != 1 ||
		p.clock().Sub(pd.started) > pendingFor {
		http.Error(w, "This sign-in has expired; start again", http.StatusNotFound)
		return
	}
	cookie.Set(w, r, &http.Cookie{Name: pendingCookie, Value: "", MaxAge: -1})
	// Shown, not retried: starting over would loop on a provider that
	// refuses every time.
	if code, refused := oidc.Refused(q); refused {
		p.Logger.Warn("admin sign-in refused by the identity provider", "error", code)
		http.Error(w, "The identity provider refused the sign-in ("+code+"). The log says more.", http.StatusForbidden)
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
	p.startSession(w, r, login.Name)
	http.Redirect(w, r, pd.ret, http.StatusSeeOther)
}

// siteLogin lets in an admin signed in on the site, once they confirmed
// it's them: whoever isn't signed in goes to the site's sign-in, and an
// admin who hasn't confirmed lately, to confirm. Both come back here.
func (p *Panel) siteLogin(w http.ResponseWriter, r *http.Request) {
	ret := safeReturn(r.URL.Query().Get("return"))
	back := "/admin/login?" + url.Values{"return": {ret}}.Encode()
	email, isAdmin, fresh := p.SiteAdmin(r)
	switch {
	case email == "":
		http.Redirect(w, r, "/api/v1/signin?"+url.Values{"return": {back}}.Encode(), http.StatusSeeOther)
	case !isAdmin:
		p.Logger.Warn("admin sign-in refused: not an admin")
		http.Error(w, "This account is not an administrator of this server", http.StatusForbidden)
	case !fresh:
		http.Redirect(w, r, "/api/v1/signin?"+url.Values{"fresh": {"1"}, "return": {back}}.Encode(), http.StatusSeeOther)
	default:
		p.startSession(w, r, email)
		http.Redirect(w, r, ret, http.StatusSeeOther)
	}
}

// startSession signs an admin in to the panel.
func (p *Panel) startSession(w http.ResponseWriter, r *http.Request, name string) {
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
	p.sessions[hashToken(token)] = &session{name: name, lastSeen: now, expires: now.Add(sessionFor)}
	p.mu.Unlock()
	cookie.Set(w, r, &http.Cookie{Name: cookieName, Value: token, MaxAge: int(sessionFor.Seconds())})
	p.Logger.Info("admin signed in")
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := cookie.Get(r, cookieName); err == nil {
		p.mu.Lock()
		delete(p.sessions, hashToken(c.Value))
		p.mu.Unlock()
	}
	cookie.Set(w, r, &http.Cookie{Name: cookieName, Value: "", MaxAge: -1})
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
	// Settings are a module's own, shown with it.
	Settings []config.Setting `json:"settings,omitempty"`
}

func (p *Panel) features(w http.ResponseWriter, r *http.Request, s *session) {
	var plugins []pluginInfo
	for name, needs := range config.KnownPlugins {
		if slices.Contains(config.AlwaysOn, name) {
			continue // nothing to switch
		}
		plugins = append(plugins, pluginInfo{Name: name, Needs: needs, Description: config.PluginDescriptions[name],
			Settings: config.PluginSettings[name]})
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

func (p *Panel) jellyfin(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Jellyfin == nil {
		http.NotFound(w, r)
		return
	}
	url, inFile := p.Jellyfin.Jellyfin()
	open, since, err := p.Jellyfin.JellyfinAccess(r.Context())
	if err != nil {
		p.Logger.Error("reading Jellyfin's access failed", "err", err)
	}
	access := map[string]any{"open": open}
	if open {
		access["since"] = since
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "inFile": inFile, "access": access})
}

func (p *Panel) catalog(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Catalog == nil {
		http.NotFound(w, r)
		return
	}
	st, err := p.Catalog.Status(r.Context())
	if err != nil {
		p.Logger.Error("reading the catalog's status failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal", "detail": "Overfin's log says why"})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (p *Panel) seerr(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Seerr == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": p.Seerr.Seerr()})
}

func (p *Panel) setSeerr(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Seerr == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		URL string `json:"url"`
		Key string `json:"key"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a valid request"})
		return
	}
	summary, err := p.Seerr.SetSeerr(r.Context(), req.URL, req.Key)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary, "url": p.Seerr.Seerr()})
}

// ratingsJSON is where Overfin's own ratings stand: the ratings country,
// those TMDB has scales for, and how many of the library's titles have
// their certifications fetched. Without a source for TMDB's data, kids
// profiles see nothing.
type ratingsJSON struct {
	Country   string   `json:"country"`
	Countries []string `json:"countries"`
	// Shown is which titles no library has are shown: all, released,
	// home (see store.TitlesShown).
	Shown   string `json:"shown"`
	Fetched int    `json:"fetched"`
	Total   int    `json:"total"`
	Source  bool   `json:"source"`
	// Token is whether the operator gave TMDB's token, and whether the
	// environment sets it.
	Token struct {
		Set   bool `json:"set"`
		InEnv bool `json:"inEnv"`
	} `json:"token"`
}

func (p *Panel) ratingsState(ctx context.Context) (ratingsJSON, error) {
	out := ratingsJSON{Source: p.TMDB != nil && p.TMDB()}
	if p.TMDBTokens != nil {
		out.Token.Set, out.Token.InEnv = p.TMDBTokens.TMDBToken()
	}
	var err error
	if out.Country, err = p.Store.RatingsCountry(ctx); err != nil {
		return out, err
	}
	if out.Countries, err = p.Store.RatingCountries(ctx); err != nil {
		return out, err
	}
	if out.Countries == nil {
		out.Countries = []string{}
	}
	if out.Shown, err = p.Store.TitlesShown(ctx); err != nil {
		return out, err
	}
	out.Fetched, out.Total, err = p.Store.RatingProgress(ctx)
	return out, err
}

func (p *Panel) ratings(w http.ResponseWriter, r *http.Request, s *session) {
	out, err := p.ratingsState(r.Context())
	if err != nil {
		p.Logger.Error("reading the ratings failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal", "detail": "Overfin's log says why"})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// titlesShown are the choices for which titles no library has are shown.
var titlesShown = []string{"all", "released", "home"}

// setRatings changes the ratings country, to one TMDB has scales for
// (kids profiles' ceilings are cleared with it), and which titles no
// library has are shown.
func (p *Panel) setRatings(w http.ResponseWriter, r *http.Request, s *session) {
	var req struct {
		Country string `json:"country"`
		Shown   string `json:"shown"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a valid request"})
		return
	}
	ctx := r.Context()
	if req.Shown != "" {
		if !slices.Contains(titlesShown, req.Shown) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a choice for the titles shown"})
			return
		}
		old, err := p.Store.TitlesShown(ctx)
		if err == nil && old != req.Shown {
			if err = p.Store.SetTitlesShown(ctx, req.Shown); err == nil {
				p.Logger.Info("the titles shown changed in the control panel", "shown", req.Shown, "by", s.name)
			}
		}
		if err != nil {
			p.Logger.Error("changing the titles shown failed", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal", "detail": "Overfin's log says why"})
			return
		}
	}
	countries, err := p.Store.RatingCountries(ctx)
	if err == nil && !slices.Contains(countries, req.Country) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "TMDB has no ratings for this country"})
		return
	}
	if err == nil {
		err = p.Store.SetRatingsCountry(ctx, req.Country)
	}
	out, err2 := p.ratingsState(ctx)
	if err == nil {
		err = err2
	}
	if err != nil {
		p.Logger.Error("changing the ratings country failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"type": "internal", "detail": "Overfin's log says why"})
		return
	}
	p.Logger.Info("ratings country changed in the control panel", "country", req.Country, "by", s.name)
	writeJSON(w, http.StatusOK, out)
}

func (p *Panel) setJellyfin(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Jellyfin == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": "not a valid request"})
		return
	}
	summary, err := p.Jellyfin.SetJellyfin(r.Context(), req.URL)
	if err != nil {
		// What's wrong, in plain words: it carries no secrets, and only
		// admins see it.
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	url, _ := p.Jellyfin.Jellyfin()
	writeJSON(w, http.StatusOK, map[string]string{"summary": summary, "url": url})
}

// setJellyfinAccess opens or closes access to Jellyfin itself. The new
// password goes to the admin who opened it, once.
func (p *Panel) setJellyfinAccess(w http.ResponseWriter, r *http.Request, s *session) {
	if p.Jellyfin == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Open bool `json:"open"`
	}
	if !decode(w, r, &req) {
		return
	}
	name, password, err := p.Jellyfin.SetJellyfinAccess(r.Context(), req.Open)
	if err != nil {
		p.upstreamProblem(w, err, "changing Jellyfin's access")
		return
	}
	if req.Open {
		p.Logger.Warn("access to Jellyfin itself was opened in the control panel", "by", s.name)
		writeJSON(w, http.StatusOK, map[string]string{"username": name, "password": password})
		return
	}
	p.Logger.Info("access to Jellyfin itself was closed in the control panel", "by", s.name)
	w.WriteHeader(http.StatusNoContent)
}

// introSkipper says whether Intro Skipper is on Overfin's Jellyfin, and
// where an install stands.
func (p *Panel) introSkipper(w http.ResponseWriter, r *http.Request, s *session) {
	if p.IntroSkipper == nil {
		http.NotFound(w, r)
		return
	}
	// While an install or a removal runs, Jellyfin restarts: the progress
	// says enough.
	progress := p.IntroSkipper.Progress()
	st := progress.Status
	if !progress.Running {
		var err error
		if st, err = p.IntroSkipper.Look(r.Context()); err != nil {
			p.upstreamProblem(w, err, "looking for Intro Skipper")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": st, "progress": progress, "version": introskipper.Version})
}

// installIntroSkipper starts Intro Skipper's install.
func (p *Panel) installIntroSkipper(w http.ResponseWriter, r *http.Request, s *session) {
	if p.IntroSkipper == nil {
		http.NotFound(w, r)
		return
	}
	if err := p.IntroSkipper.Start(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	p.Logger.Info("Intro Skipper's install started in the control panel", "by", s.name)
	writeJSON(w, http.StatusAccepted, p.IntroSkipper.Progress())
}

// removeIntroSkipper starts removing Intro Skipper.
func (p *Panel) removeIntroSkipper(w http.ResponseWriter, r *http.Request, s *session) {
	if p.IntroSkipper == nil {
		http.NotFound(w, r)
		return
	}
	if err := p.IntroSkipper.StartRemoving(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"type": "invalid-request", "detail": err.Error()})
		return
	}
	p.Logger.Info("Intro Skipper's removal started in the control panel", "by", s.name)
	writeJSON(w, http.StatusAccepted, p.IntroSkipper.Progress())
}

// setTMDBToken sets or removes the operator's TMDB token.
func (p *Panel) setTMDBToken(w http.ResponseWriter, r *http.Request, s *session) {
	if p.TMDBTokens == nil {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	summary, err := p.TMDBTokens.SetTMDBToken(r.Context(), req.Token)
	if err != nil {
		p.upstreamProblem(w, err, "setting TMDB's token")
		return
	}
	out, err := p.ratingsState(r.Context())
	if err != nil {
		p.fail(w, "reading TMDB's state", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": summary, "state": out})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- Modules' tabs ------------------------------------------------------------

func (p *Panel) tabs() []Tab {
	if p.Modules == nil {
		return nil
	}
	return p.Modules.Tabs()
}

func (p *Panel) moduleTabs(w http.ResponseWriter, r *http.Request, _ *session) {
	type tabJSON struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		HasTab      bool   `json:"hasTab"`
	}
	out := []tabJSON{}
	for _, t := range p.tabs() {
		out = append(out, tabJSON{Name: t.Name, Description: t.Description, HasTab: t.UI != nil})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// moduleFile serves a file of a switched-on module's tab.
func (p *Panel) moduleFile(w http.ResponseWriter, r *http.Request, _ *session) {
	for _, t := range p.tabs() {
		if t.Name == r.PathValue("name") && t.UI != nil {
			http.StripPrefix("/admin/modules/"+t.Name+"/", http.FileServerFS(t.UI)).ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
}
