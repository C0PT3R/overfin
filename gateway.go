package overfin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/gpu"
	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/jfsettings"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/mail"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/pin"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/plugin"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/ratings"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/server"
	"github.com/overfin/server/internal/site"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/takeover"
	"github.com/overfin/server/internal/tmdb"
)

// shared is what lives for the whole run, across configuration reloads: the
// database, the keys, the Jellyfin client, the image cache and the
// playbacks in progress.
type shared struct {
	logger *slog.Logger
	db     *store.Store
	// moduleDBs are the modules' own databases, by module name.
	moduleDBs map[string]*sql.DB
	keys      *keys.Keys
	jf        *jellyfin.Client
	images    *images.Pipeline
	tickets   *playback.Tickets
	// folders remembers which library items the index doesn't hold are in.
	folders *api.FolderCache
	// service is Overfin's one Jellyfin account, shared by everything: a
	// second sign-in on its device would cancel the first one's token.
	service *provision.Service
	// jellyfinSaved: Jellyfin's settings are the setup page's, in the
	// database, not the configuration's, so the panel may change them.
	jellyfinSaved bool
	// seerr is Seerr, when the panel connected it (nil otherwise); with
	// it, discovery is on. Changed under the gateway's lock.
	seerr    *seerr.Client
	seerrURL string
	seerrKey string
	// tmdb is TMDB itself, with the Overfin project's key: TMDB's data
	// comes from it first, from Seerr otherwise.
	tmdb *tmdb.Client
	// lists keeps discovery's lists from TMDB, in the background.
	lists *catalog.Lists
	// ratings keeps titles' certifications, in the background.
	ratings *ratings.Keeper
	// titles keeps what TMDB says about the library's titles, in the background.
	titles *catalog.Titles
	// searches limits searches per account, across reloads.
	searches *api.SearchLimiter
	// requested is what Seerr says anyone requested, kept across reloads.
	requested *api.SeerrRequested
	// directory is the identity provider's connector, when configured:
	// who the people behind accounts are, kept briefly in memory.
	directory *connector.Cache
	// mail is the SMTP server emails go through, nil for none: the
	// configuration file's (mailInFile), or the control panel's.
	mail       atomic.Pointer[mail.Sender]
	mailInFile bool
}

// mailer is the SMTP server in effect, nil for none.
func (sh *shared) mailer() api.Mailer {
	if s := sh.mail.Load(); s != nil {
		return s
	}
	return nil // not a nil *mail.Sender: that wouldn't be a nil Mailer
}

// senderFor is the SMTP server with these settings.
func senderFor(m config.Mail, password string) *mail.Sender {
	return &mail.Sender{Host: m.Host, Port: m.Port, TLS: m.TLS, From: m.From, Username: m.Username, Password: password}
}

func newProvisioner(cfg *config.Config, sh *shared) *provision.Provisioner {
	p := &provision.Provisioner{
		Store:         sh.db,
		Jellyfin:      sh.jf,
		AdminKey:      cfg.Secrets.JellyfinAdminKey,
		RequiredGroup: cfg.Identity.RequiredGroup,
		AdminGroup:    adminGroup(cfg),
		LogKey:        sh.keys.LogID,
		Logger:        sh.logger,
	}
	// A deleted account's Seerr user goes with it, through the Seerr
	// connected now (connecting or disconnecting one builds again).
	if client := sh.seerr; client != nil {
		p.DeleteSeerrUser = client.DeleteUser
	}
	return p
}

// build makes the request handler for a configuration.
// built is what build makes from a configuration: the public handler, and
// the switched-on modules' control panel routes and tabs.
type built struct {
	handler http.Handler
	admin   *http.ServeMux
	tabs    []admin.Tab
	// managed is the managed sign-in, when that's the identity mode.
	managed *api.Managed
}

func build(cfg *config.Config, sh *shared) (*built, error) {
	logger, db, k := sh.logger, sh.db, sh.keys
	if cfg.Mail.Enabled() {
		sh.mail.Store(senderFor(cfg.Mail, cfg.Secrets.MailPassword))
		sh.mailInFile = true
	}
	var ids *identity.Resolver
	switch cfg.Identity.Mode {
	case "forwardauth":
		ids = identity.NewForwardAuth(cfg.Identity.Proxies, cfg.Identity.UserHeader, cfg.Identity.GroupsHeader, cfg.Identity.GroupsSeparator)
	case "anonymous":
		ids = identity.NewAnonymous(cfg.Identity.Proxies)
	case "oidc", "managed":
		ids = identity.NewHeaderless(cfg.Identity.Proxies)
	default:
		return nil, fmt.Errorf("identity mode %q is not supported", cfg.Identity.Mode)
	}
	prov := newProvisioner(cfg, sh)

	sessions := &api.Sessions{Provisioner: prov, Store: db, Logger: logger, Lifetime: 30 * 24 * time.Hour}
	checker := &pin.Checker{Key: k.PIN, Store: db, MaxFailures: cfg.Limits.PINAttempts}
	identityMiddleware := ids.Middleware
	// Overfin's own sign-in, and step-up, when it has them.
	var signIn []plugin.Plugin
	if o := cfg.OIDC; o.Enabled() {
		needGroups := cfg.Identity.Mode == "oidc" && cfg.Identity.RequiredGroup != ""
		groupsClaim := ""
		if needGroups || adminGroup(cfg) != "" {
			groupsClaim = o.GroupsClaim
		}
		auth := &api.Auth{Sessions: sessions, Store: db, Provisioner: prov, Logger: logger,
			FreshFor:      time.Duration(o.FreshForMinutes) * time.Minute,
			Login:         cfg.Identity.Mode == "oidc",
			LoginLifetime: 30 * 24 * time.Hour,
			Recheck:       time.Duration(o.RecheckMinutes) * time.Minute,
			NeedGroups:    needGroups,
			AdminGroup:    adminGroup(cfg),
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: o.RedirectURL, GroupsClaim: groupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		sessions.StepUpURL, sessions.FreshFor = api.StepUpPath, auth.FreshFor
		signIn = append(signIn, auth)
		if auth.Login {
			sessions.SignIn = auth
			// The login cookie sets the identity; the headerless resolver
			// only finds the client address.
			identityMiddleware = func(h http.Handler) http.Handler { return ids.Middleware(auth.Middleware(h)) }
		}
	}
	var managed *api.Managed
	if cfg.Identity.Mode == "managed" {
		managed = &api.Managed{Sessions: sessions, Store: db, Provisioner: prov, CodeKey: k.SigninCode,
			Mail: sh.mailer, SiteURL: siteURL(cfg), LoginLifetime: 30 * 24 * time.Hour, FreshFor: 5 * time.Minute,
			Logger: logger}
		sessions.StepUpURL, sessions.FreshFor, sessions.SignIn = api.StepUpPath, managed.FreshFor, managed
		signIn = append(signIn, managed)
		identityMiddleware = func(h http.Handler) http.Handler { return ids.Middleware(managed.Middleware(h)) }
	}
	if sessions.StepUpURL == "" {
		// Without step-up the owner can't confirm a login, so PIN locks
		// end by themselves.
		checker.UnlockAfter = 15 * time.Minute
	}
	lib := api.Library{Sessions: sessions, Provisioner: prov, Jellyfin: sh.jf, Images: sh.images, CursorKey: k.Cursor,
		Requested: sh.requested,
		Folders:   sh.folders, Service: sh.service, Titles: sh.titles, Searches: sh.searches, Logger: logger}
	// Discovery, when there's a source for TMDB's data.
	var disc *api.Discovery
	if sh.source() != nil {
		disc = &api.Discovery{Library: lib, Store: db}
	}
	pb := api.Playback{Library: lib, Tickets: sh.tickets,
		MaxBitrateKbps: cfg.Limits.MaxBitrateKbps, VersionNames: cfg.Privacy.Exposes("version.names"),
		MaxTranscodes:   cfg.Limits.MaxTranscodesTotal,
		RefuseOverLimit: cfg.Limits.OnLimit == "refuse", Logger: logger}
	// The modules the operator switched on, with their control panel routes
	// and tabs.
	b := &built{admin: http.NewServeMux()}
	var mounts []api.Mount
	for _, m := range modules {
		info := m.Info()
		if cfg.Plugins.Enabled(info.Name) {
			mounts = append(mounts, api.Mount{Module: m, Library: lib, DB: sh.moduleDBs[info.Name], Logger: logger, Admin: b.admin,
				Settings: cfg.Modules[info.Name], SiteURL: siteURL(cfg), SignUp: signUp(cfg)})
			b.tabs = append(b.tabs, admin.Tab{Name: info.Name, Description: info.Description, UI: info.AdminUI})
		}
	}
	for i := range mounts {
		mounts[i].Peers = mounts
	}
	offers, err := api.SetupOffers(mounts)
	if err != nil {
		return nil, err
	}
	sessions.SetupOffers = offers
	if managed != nil {
		managed.Mounts = mounts // newcomers' invitations
		b.managed = managed
	}
	// Accounts and profiles are always on; everything else is a plugin the
	// operator enables.
	plugins := []plugin.Plugin{
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, Mounts: mounts,
			KeepOnDelete: cfg.Accounts.OnDelete == "disable", Images: sh.images, PINs: checker},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: db, Service: sh.service,
			PINs: checker, Images: sh.images, LogoutURL: cfg.Identity.LogoutURL, Logger: logger},
	}
	plugins = append(plugins, signIn...)
	if cfg.Plugins.Enabled("library") {
		plugins = append(plugins, lib)
	}
	if cfg.Plugins.Enabled("playback") {
		plugins = append(plugins, pb, api.Stream{Playback: pb})
	}
	if disc != nil {
		plugins = append(plugins, disc)
	}
	// Requests, through the Seerr connected when this was built (connecting
	// or disconnecting Seerr builds again); without one, they answer "not
	// found".
	if cfg.Plugins.Enabled("library") {
		client := sh.seerr
		if sh.requested != nil && client != nil {
			sh.requested.SetClient(func() *seerr.Client { return client })
		} else if sh.requested != nil {
			sh.requested.SetClient(nil)
		}
		rq := api.Requests{Library: lib, Seerr: func() *seerr.Client { return client }}
		if sh.directory != nil {
			rq.Directory = sh.directory
		}
		plugins = append(plugins, rq)
	}
	// Modules go behind the same guards as the core's own routes.
	for _, m := range mounts {
		plugins = append(plugins, m)
	}
	logger.Info("plugins", "enabled", cfg.Plugins.Enable)
	siteHandler, err := newSite(cfg.Site, api.SiteGate(sessions.SignIn, prov))
	if err != nil {
		return nil, err
	}
	h, err := server.New(logger, identityMiddleware, siteHandler, plugins...)
	if err != nil {
		return nil, err
	}
	b.handler = h
	return b, nil
}

// newSite is the website Overfin serves beside the API: the one this build
// includes, the operator's own client from a folder, or none (nil).
func newSite(s config.Site, gate site.Gate) (http.Handler, error) {
	switch s.Serve {
	case "none":
		return nil, nil
	case "builtin":
		if builtinSite == nil {
			return nil, nil // this build includes none
		}
		// A build's client is a single-page app with real paths
		// (/item/…): every page is its index.html.
		return site.Embedded(builtinSite, s.CSP, true, gate)
	default:
		return site.Folder(s.Serve, s.CSP, s.SPAFallback, gate)
	}
}

// siteURL is the site's public address, wherever a link to it is made
// (emails, invitations, sign-in links): site.url, else the domain Overfin
// gets a certificate for, else the identity provider's return address;
// empty when none says.
func siteURL(cfg *config.Config) string {
	if cfg.Site.URL != "" {
		return strings.TrimSuffix(cfg.Site.URL, "/")
	}
	if cfg.HTTPS.Enabled() {
		return "https://" + cfg.HTTPS.Domain
	}
	u, err := url.Parse(cfg.OIDC.RedirectURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return "https://" + u.Host
}

// effective completes a configuration read from the file with the feature
// settings, which live in the database. On the first start they come from
// the file's feature sections, or the defaults, and are saved; after that
// the database holds them, and the file's are ignored.
func effective(ctx context.Context, cfg *config.Config, db *store.Store, logger *slog.Logger) (*config.Config, error) {
	raw, stored, err := db.Features(ctx)
	if err != nil {
		return nil, err
	}
	f := cfg.Features()
	if stored {
		f = config.DefaultFeatures()
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("feature settings in the database: %w", err)
		}
		// Settings saved before bringing accounts over became the
		// migration module's are carried over to it, once.
		var former struct {
			Adoption *config.Adoption `json:"adoption"`
		}
		if err := json.Unmarshal(raw, &former); err == nil && former.Adoption != nil {
			f.ImportAdoption(*former.Adoption)
			n, err := cfg.WithFeatures(f)
			if err != nil {
				return nil, err
			}
			if raw, err = json.Marshal(n.Features()); err != nil {
				return nil, err
			}
			if err := db.SetFeatures(ctx, raw); err != nil {
				return nil, err
			}
			logger.Info("bringing existing accounts over is the migration module's now; its settings were carried over")
		}
		if cfg.FeaturesInFile {
			logger.Warn("the configuration file's feature settings are ignored: the control panel holds them; " +
				"remove [limits], [adoption], [privacy], [plugins], [accounts] and [modules] from the file")
		}
	} else {
		if raw, err = json.Marshal(f); err != nil {
			return nil, err
		}
		if err := db.SetFeatures(ctx, raw); err != nil {
			return nil, err
		}
		if cfg.FeaturesInFile {
			logger.Info("feature settings imported from the configuration file into the database; " +
				"the control panel holds them from now on, so their sections can be removed from the file")
		}
	}
	return cfg.WithFeatures(f)
}

// gateway serves every request with the handler built from the current
// configuration, and swaps it when the configuration is reloaded. Requests
// already running finish on the handler they started with.
type gateway struct {
	path string
	sh   *shared

	mu      sync.Mutex // one reload at a time
	mailMu  sync.Mutex // one email change at a time
	gpuMu   sync.Mutex // one transcoding test or change at a time
	gpuTest atomic.Pointer[admin.GPUTest]
	cfg     *config.Config
	current atomic.Pointer[built]
}

func newGateway(path string, cfg *config.Config, sh *shared) (*gateway, error) {
	cfg, err := effective(context.Background(), cfg, sh.db, sh.logger)
	if err != nil {
		return nil, err
	}
	b, err := build(cfg, sh)
	if err != nil {
		return nil, err
	}
	g := &gateway{path: path, sh: sh, cfg: cfg}
	g.current.Store(b)
	return g, nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.current.Load().handler.ServeHTTP(w, r)
}

// SiteAdmin is who's signed in with managed sign-in, for the control
// panel on the site's address (api.Managed.SiteAdmin); nobody in other
// modes.
func (g *gateway) SiteAdmin(r *http.Request) (email string, admin, fresh bool) {
	if m := g.current.Load().managed; m != nil {
		return m.SiteAdmin(r)
	}
	return "", false, false
}

// SigninLink makes a one-time sign-in link, valid a week, that an admin
// hands to someone (managed sign-in only): on the site's address, or a
// path when it isn't known.
func (g *gateway) SigninLink(ctx context.Context, email string) (string, error) {
	m := g.current.Load().managed
	if m == nil {
		return "", errors.New("sign-in links need managed sign-in")
	}
	path, err := m.SigninLink(ctx, email, 7*24*time.Hour)
	if err != nil {
		return "", err
	}
	g.mu.Lock()
	site := siteURL(g.cfg)
	g.mu.Unlock()
	return site + path, nil
}

// AdminRoutes serves the switched-on modules' control panel routes.
func (g *gateway) AdminRoutes() http.Handler { return g.current.Load().admin }

// Tabs lists the switched-on modules, for the control panel.
func (g *gateway) Tabs() []admin.Tab {
	return g.current.Load().tabs
}

// reload reads the configuration again and, if it's valid and needs no
// restart, switches to it. Otherwise nothing changes and the error says why.
func (g *gateway) reload(getenv func(string) string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg, err := config.Load(g.path, getenv)
	if err != nil {
		return err
	}
	// Jellyfin saved by the setup page isn't in the file: it stays.
	if cfg.Jellyfin.URL == "" {
		cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = g.cfg.Jellyfin.URL, g.cfg.Secrets.JellyfinAdminKey
	}
	if cfg, err = effective(context.Background(), cfg, g.sh.db, g.sh.logger); err != nil {
		return err
	}
	if changed := restartOnly(g.cfg, cfg); len(changed) > 0 {
		return fmt.Errorf("these settings only change with a restart: %s", strings.Join(changed, ", "))
	}
	b, err := build(cfg, g.sh)
	if err != nil {
		return err
	}
	g.current.Store(b)
	g.cfg = cfg
	return nil
}

// Provisioner is the provisioner of the configuration in effect.
func (g *gateway) Provisioner() *provision.Provisioner {
	g.mu.Lock()
	defer g.mu.Unlock()
	return newProvisioner(g.cfg, g.sh)
}

// Jellyfin is Jellyfin's address in effect, and whether the configuration
// file sets it (then only the file changes it).
func (g *gateway) Jellyfin() (url string, inFile bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Jellyfin.URL, !g.sh.jellyfinSaved
}

// SetJellyfin changes the address Overfin reaches its Jellyfin at, tested
// first and applied at once. Only the same server is accepted, answering to
// Overfin's own key: another one isn't Overfin's (SECURITY.md rule 67).
// Errors say what's wrong in plain words.
func (g *gateway) SetJellyfin(ctx context.Context, url string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.sh.jellyfinSaved {
		return "", problem("Jellyfin is set in Overfin's configuration file: change it there.")
	}
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	key := g.cfg.Secrets.JellyfinAdminKey
	found, err := testJellyfin(ctx, url, key)
	if err != nil {
		return "", err
	}
	saved, err := g.sh.db.JellyfinServer(ctx)
	if err != nil {
		return "", err
	}
	if saved != "" && found.serverID != saved {
		return "", problem("That's another Jellyfin server. Only a new address for the same one can be set here.")
	}
	cfg := *g.cfg
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	b, err := build(&cfg, g.sh)
	if err != nil {
		return "", err
	}
	if err := g.sh.db.SetJellyfin(ctx, url, key, found.serverID); err != nil {
		return "", err
	}
	if err := g.sh.jf.SetURL(url); err != nil {
		return "", err
	}
	g.current.Store(b)
	g.cfg = &cfg
	g.sh.logger.Info("Jellyfin changed in the control panel", "jellyfin", url)
	return found.summary, nil
}

// JellyfinAccess tells whether access to Jellyfin itself is open, and
// since when.
func (g *gateway) JellyfinAccess(ctx context.Context) (bool, time.Time, error) {
	return g.sh.db.JellyfinAccess(ctx)
}

// SetJellyfinAccess opens access to Jellyfin itself, giving Overfin's
// administrator account there a new password (returned, shown once, never
// kept), or closes it: every administrator gets a password nobody knows
// (SECURITY.md rule 67).
func (g *gateway) SetJellyfinAccess(ctx context.Context, open bool) (name, password string, err error) {
	a := g.adminAuth()
	if open {
		name, password, err = takeover.OpenAccess(ctx, g.sh.jf, a)
	} else {
		err = takeover.CloseAccess(ctx, g.sh.jf, a)
	}
	if err != nil {
		return "", "", forPage(err)
	}
	return name, password, g.sh.db.SetJellyfinAccess(ctx, open)
}

// adminAuth is the key Overfin made on its Jellyfin.
func (g *gateway) adminAuth() jellyfin.Auth {
	g.mu.Lock()
	defer g.mu.Unlock()
	return jellyfin.Auth{Token: g.cfg.Secrets.JellyfinAdminKey, DeviceID: "overfin-admin"}
}

// Transcoding is the GPU family chosen, and its device.
func (g *gateway) Transcoding(ctx context.Context) (family, device string, err error) {
	return g.sh.db.Transcoding(ctx)
}

// TestTranscoding starts testing a GPU family on a few videos, in the
// background: TranscodingTest says how it goes. The settings it tries are
// put back after.
func (g *gateway) TestTranscoding(ctx context.Context, family, device string) error {
	f, ok := gpu.Find(family)
	if !ok {
		return admin.Problem("Overfin doesn't know that GPU.")
	}
	if !g.gpuMu.TryLock() {
		return admin.Problem("A test is running already.")
	}
	state := admin.GPUTest{Family: f.ID, Device: device, Running: true}
	g.gpuTest.Store(&state)
	go func() {
		defer g.gpuMu.Unlock()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		_, err := gpu.Test(ctx, g.sh.jf, g.adminAuth(), f, device, func(total int, done []gpu.Result) {
			next := state
			next.Total, next.Results = total, done
			g.gpuTest.Store(&next)
		})
		final := *g.gpuTest.Load()
		final.Running = false
		var p gpu.Problem
		switch {
		case errors.As(err, &p):
			final.Error = string(p)
		case err != nil:
			g.sh.logger.Error("testing a GPU failed", "err", err)
			final.Error = "The test failed; Overfin's log says why."
		}
		g.gpuTest.Store(&final)
	}()
	return nil
}

// TranscodingTest is how the last test went, or goes; nil before any.
func (g *gateway) TranscodingTest() *admin.GPUTest { return g.gpuTest.Load() }

// SetTranscoding saves the GPU family chosen and sets Jellyfin's
// transcoding to it now; it's set back at every start too.
func (g *gateway) SetTranscoding(ctx context.Context, family, device string) error {
	f, ok := gpu.Find(family)
	if !ok {
		return admin.Problem("Overfin doesn't know that GPU.")
	}
	if !g.gpuMu.TryLock() {
		return admin.Problem("A test is running: wait for it.")
	}
	defer g.gpuMu.Unlock()
	if err := g.sh.db.SetTranscoding(ctx, f.ID, device); err != nil {
		return err
	}
	changes, err := jfsettings.Apply(ctx, g.sh.jf, g.adminAuth(), f.Settings(device)...)
	for _, c := range changes {
		g.sh.logger.Info("a Jellyfin setting was set back to Overfin's", "change", c.String())
	}
	return err
}

// source is where TMDB's data comes from: TMDB itself, with the project's
// key, or else Seerr, when it's connected; nil when there's neither.
func (sh *shared) source() tmdb.Source {
	if sh.tmdb != nil {
		return sh.tmdb
	}
	if sh.seerr != nil {
		return sh.seerr
	}
	return nil
}

// tmdbSource is the current source of TMDB's data, for the keepers.
func (g *gateway) tmdbSource() tmdb.Source {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sh.source()
}

// titleSource is where the library's titles are described: TMDB itself.
func (g *gateway) titleSource() catalog.TitleSource {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.sh.tmdb != nil {
		return g.sh.tmdb
	}
	return nil
}

// ratingSource is where certifications come from.
func (g *gateway) ratingSource() ratings.Source {
	if src := g.tmdbSource(); src != nil {
		return ratings.TMDB{Source: src}
	}
	return nil
}

// Seerr is Seerr's address, when it's connected.
func (g *gateway) Seerr() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sh.seerrURL
}

// SetSeerr connects Seerr, or changes its address or key (an empty key
// keeps the current one), tested first and applied at once; with neither,
// it disconnects Seerr. Errors say what's wrong in plain words.
func (g *gateway) SetSeerr(ctx context.Context, url, key string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	url = strings.TrimRight(strings.TrimSpace(url), "/")
	if key = strings.TrimSpace(key); key == "" {
		key = g.sh.seerrKey
	}
	var client *seerr.Client
	if url != "" {
		var err error
		if client, err = seerr.New(url, key); err != nil {
			return "", problem("That isn't an address: use http://… or https://….")
		}
		switch _, err := client.Check(ctx); {
		case errors.Is(err, seerr.ErrUnauthorized):
			return "", problem("Seerr refuses this API key. It's in Seerr's settings, under General.")
		case err != nil:
			return "", problemf("Seerr: can't reach it at %s (%v). It's reached from where Overfin runs: "+
				"in a container, localhost is the container itself", url, err)
		}
	}

	// Build with the new Seerr first: nothing changes if that fails.
	old, oldURL, oldKey := g.sh.seerr, g.sh.seerrURL, g.sh.seerrKey
	if client == nil {
		key = ""
	}
	g.sh.seerr, g.sh.seerrURL, g.sh.seerrKey = client, url, key
	b, err := build(g.cfg, g.sh)
	if err == nil {
		if client == nil {
			// TMDB's data goes with it, as its terms ask, unless it comes
			// from TMDB itself.
			if err = g.sh.db.ForgetSeerr(ctx); err == nil && g.sh.tmdb == nil {
				err = g.sh.db.ForgetTMDB(ctx)
			}
		} else {
			err = g.sh.db.SetSeerr(ctx, url, key)
		}
	}
	if err != nil {
		g.sh.seerr, g.sh.seerrURL, g.sh.seerrKey = old, oldURL, oldKey
		return "", err
	}
	g.current.Store(b)
	if client == nil {
		g.sh.logger.Info("Seerr disconnected in the control panel")
		if g.sh.tmdb != nil {
			return "Seerr is disconnected.", nil
		}
		return "Seerr is disconnected: discovery is off.", nil
	}
	g.sh.logger.Info("Seerr connected in the control panel", "seerr", url)
	if g.sh.lists != nil {
		g.sh.lists.Wake() // discovery's lists, read now
	}
	if g.sh.ratings != nil {
		g.sh.ratings.Wake()
	}
	if g.sh.tmdb != nil {
		return "Seerr, at " + url + ": its API key works.", nil
	}
	return "Seerr, at " + url + ": its API key works. Discovery is on.", nil
}

// Features returns the feature settings in effect.
func (g *gateway) Features() config.Features {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cfg.Features()
}

// SetFeatures validates new feature settings, saves them and switches to
// them at once. Invalid ones change nothing; the error says why.
func (g *gateway) SetFeatures(ctx context.Context, f config.Features) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	cfg, err := g.cfg.WithFeatures(f)
	if err != nil {
		return err
	}
	b, err := build(cfg, g.sh)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(cfg.Features())
	if err != nil {
		return err
	}
	if err := g.sh.db.SetFeatures(ctx, raw); err != nil {
		return err
	}
	g.current.Store(b)
	g.cfg = cfg
	return nil
}

// restartOnly lists the settings that differ between old and new but can't
// change while running: the address Overfin listens on, its database, the
// Jellyfin server its playbacks run on, and the key its data is sealed with.
func restartOnly(old, new *config.Config) []string {
	var changed []string
	if old.Listen != new.Listen {
		changed = append(changed, "listen")
	}
	if old.Storage.Path != new.Storage.Path {
		changed = append(changed, "storage.path")
	}
	if old.Jellyfin.URL != new.Jellyfin.URL {
		changed = append(changed, "jellyfin.url")
	}
	if old.Admin != new.Admin {
		changed = append(changed, "admin")
	}
	if old.HTTPS != new.HTTPS || old.Site.URL != new.Site.URL {
		changed = append(changed, "https, site.url")
	}
	if old.Connector != new.Connector || old.Secrets.ConnectorToken != new.Secrets.ConnectorToken {
		changed = append(changed, "connector")
	}
	if !bytes.Equal(old.Secrets.Key, new.Secrets.Key) {
		changed = append(changed, "OVERFIN_SECRET_KEY")
	}
	return changed
}

// signUp makes invitations' sign-up links at Pocket ID, whose accounts join
// the group the site requires; nil with other providers.
func signUp(cfg *config.Config) func(ctx context.Context, previous string, uses int, until time.Time) (string, string, error) {
	cn := cfg.Connector
	if cn.Kind != "pocketid" || !cfg.OIDC.Enabled() {
		return nil
	}
	p := &connector.PocketID{BaseURL: cn.URL, Key: cfg.Secrets.ConnectorToken, HTTP: oidc.NewClient(cn.Dial)}
	var groups []string
	if g := cfg.Identity.RequiredGroup; g != "" {
		groups = append(groups, g)
	}
	// Pocket ID's issuer is its public address, where people go.
	link := func(t connector.SignupToken) string {
		return strings.TrimSuffix(cfg.OIDC.Issuer, "/") + "/st/" + url.PathEscape(t.Token)
	}
	return func(ctx context.Context, previous string, uses int, until time.Time) (string, string, error) {
		if previous != "" {
			t, err := p.FindSignupToken(ctx, previous)
			switch {
			case err == nil && t.UsedUp:
				return "", t.ID, nil
			case err == nil:
				return link(t), t.ID, nil
			case !errors.Is(err, connector.ErrNotFound):
				return "", "", err
			}
		}
		t, err := p.NewSignupToken(ctx, uses, time.Until(until), groups)
		if err != nil {
			return "", "", err
		}
		return link(t), t.ID, nil
	}
}

// Mail is the SMTP server's settings in effect, whether it has a password,
// and whether the configuration file sets them (then only the file changes
// them).
func (g *gateway) Mail() (m config.Mail, hasPassword, inFile bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.sh.mail.Load()
	if s == nil {
		return config.Mail{}, false, g.sh.mailInFile
	}
	return config.Mail{Host: s.Host, Port: s.Port, TLS: s.TLS, From: s.From, Username: s.Username},
		s.Password != "", g.sh.mailInFile
}

// SetMail changes the SMTP server, after a test email to testTo went
// through; an empty host turns emails off. An empty password keeps the
// current one, for the same server and username. Errors say what's wrong in
// plain words.
func (g *gateway) SetMail(ctx context.Context, m config.Mail, password, testTo string) (string, error) {
	// One change at a time, apart from the gateway's lock: a test email
	// can take a while.
	g.mailMu.Lock()
	defer g.mailMu.Unlock()
	g.mu.Lock()
	inFile := g.sh.mailInFile
	g.mu.Unlock()
	if inFile {
		return "", problem("Email is set in Overfin's configuration file: change it there.")
	}
	m.Host, m.From, m.Username = strings.TrimSpace(m.Host), strings.TrimSpace(m.From), strings.TrimSpace(m.Username)
	if m.Host == "" {
		if err := g.sh.db.ForgetMail(ctx); err != nil {
			return "", err
		}
		g.sh.mail.Store(nil)
		g.sh.logger.Info("email turned off in the control panel")
		return "Email is off: people sign in with a password, or a sign-in link you hand them.", nil
	}
	m.Defaults()
	if err := m.Check(); err != nil {
		return "", problem(strings.ReplaceAll(err.Error(), "mail.", ""))
	}
	if old := g.sh.mail.Load(); password == "" && old != nil && old.Host == m.Host && old.Username == m.Username {
		password = old.Password
	}
	if m.Username != "" && password == "" {
		return "", problem("With a username, the SMTP server's password is needed too.")
	}
	s := senderFor(m, password)
	if err := s.Send(ctx, testTo, "Overfin can send emails",
		"This test comes from Overfin's control panel: sign-in codes will arrive like it.\n"); err != nil {
		return "", problemf("A test email to %s didn't go through, so nothing changed: %v", testTo, err)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if err := g.sh.db.SetMail(ctx, raw, password); err != nil {
		return "", err
	}
	g.sh.mail.Store(s)
	g.sh.logger.Info("email set up in the control panel", "smtp", m.Host)
	return "Saved: a test email went to " + testTo + ".", nil
}

// adminGroup is the control panel's group, when people log in to the site
// with Overfin's own login: the site learns who's in it from their login.
// With managed sign-in, admins are marked as such, in a group of its own.
func adminGroup(cfg *config.Config) string {
	if cfg.Identity.Mode == "managed" {
		return api.ManagedAdminGroup
	}
	if !cfg.Admin.Enabled() || cfg.Identity.Mode != "oidc" {
		return ""
	}
	return cfg.Admin.Group
}
