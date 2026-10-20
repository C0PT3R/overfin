package overfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/C0PT3R/overfin/internal/admin"
	"github.com/C0PT3R/overfin/internal/api"
	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/images"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/keys"
	"github.com/C0PT3R/overfin/internal/oidc"
	"github.com/C0PT3R/overfin/internal/pin"
	"github.com/C0PT3R/overfin/internal/playback"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/server"
	"github.com/C0PT3R/overfin/internal/store"
)

// shared is what lives for the whole run, across configuration reloads: the
// database, the keys, the Jellyfin client, the image cache and the
// playbacks in progress.
type shared struct {
	logger  *slog.Logger
	db      *store.Store
	keys    *keys.Keys
	jf      *jellyfin.Client
	images  *images.Pipeline
	tickets *playback.Tickets
}

func newProvisioner(cfg *config.Config, sh *shared) *provision.Provisioner {
	return &provision.Provisioner{
		Store:         sh.db,
		Jellyfin:      sh.jf,
		AdminKey:      cfg.Secrets.JellyfinAdminKey,
		RequiredGroup: cfg.Identity.RequiredGroup,
		LogKey:        sh.keys.LogID,
		Logger:        sh.logger,
	}
}

// build makes the request handler for a configuration.
// built is what build makes from a configuration: the public handler, and
// the switched-on modules' control panel routes and tabs.
type built struct {
	handler http.Handler
	admin   *http.ServeMux
	tabs    []admin.Tab
}

func build(cfg *config.Config, sh *shared) (*built, error) {
	logger, db, k := sh.logger, sh.db, sh.keys
	var ids *identity.Resolver
	switch cfg.Identity.Mode {
	case "forwardauth":
		ids = identity.NewForwardAuth(cfg.Identity.Proxies, cfg.Identity.UserHeader, cfg.Identity.GroupsHeader, cfg.Identity.GroupsSeparator)
	case "anonymous":
		ids = identity.NewAnonymous(cfg.Identity.Proxies)
	case "oidc":
		ids = identity.NewHeaderless(cfg.Identity.Proxies)
	default:
		return nil, fmt.Errorf("identity mode %q is not supported", cfg.Identity.Mode)
	}
	prov := newProvisioner(cfg, sh)

	sessions := &api.Sessions{Provisioner: prov, Store: db, Logger: logger, Lifetime: 30 * 24 * time.Hour}
	checker := &pin.Checker{Key: k.PIN, Store: db, MaxFailures: cfg.Limits.PINAttempts}
	identityMiddleware := ids.Middleware
	if o := cfg.OIDC; o.Enabled() {
		needGroups := cfg.Identity.Mode == "oidc" && cfg.Identity.RequiredGroup != ""
		groupsClaim := ""
		if needGroups {
			groupsClaim = o.GroupsClaim
		}
		auth := &api.Auth{Sessions: sessions, Store: db, Provisioner: prov, Logger: logger,
			FreshFor:      time.Duration(o.FreshForMinutes) * time.Minute,
			Login:         cfg.Identity.Mode == "oidc",
			LoginLifetime: 30 * 24 * time.Hour,
			Recheck:       time.Duration(o.RecheckMinutes) * time.Minute,
			NeedGroups:    needGroups,
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: o.RedirectURL, GroupsClaim: groupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		sessions.Auth = auth
		if auth.Login {
			// The login cookie sets the identity; the headerless resolver
			// only finds the client address.
			identityMiddleware = func(h http.Handler) http.Handler { return ids.Middleware(auth.Middleware(h)) }
		}
	} else {
		// Without step-up the owner can't confirm a login, so PIN locks
		// end by themselves.
		checker.UnlockAfter = 15 * time.Minute
	}
	lib := api.Library{Sessions: sessions, Provisioner: prov, Jellyfin: sh.jf, Images: sh.images, CursorKey: k.Cursor, Logger: logger}
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
			mounts = append(mounts, api.Mount{Module: m, Library: lib, DB: db.DB(), Logger: logger, Admin: b.admin,
				Settings: cfg.Modules[info.Name], SiteURL: siteURL(cfg)})
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
	// Accounts and profiles are always on; everything else is a plugin the
	// operator enables.
	plugins := []plugin.Plugin{
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, Mounts: mounts,
			KeepOnDelete: cfg.Accounts.OnDelete == "disable", Images: sh.images, PINs: checker},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: db,
			PINs: checker, Images: sh.images, LogoutURL: cfg.Identity.LogoutURL, Logger: logger},
	}
	if sessions.Auth != nil {
		plugins = append(plugins, sessions.Auth)
	}
	if cfg.Plugins.Enabled("library") {
		plugins = append(plugins, lib)
	}
	if cfg.Plugins.Enabled("playback") {
		plugins = append(plugins, pb, api.Stream{Playback: pb})
	}
	// Modules go behind the same guards as the core's own routes.
	for _, m := range mounts {
		plugins = append(plugins, m)
	}
	logger.Info("plugins", "enabled", cfg.Plugins.Enable)
	h, err := server.New(logger, identityMiddleware, plugins...)
	if err != nil {
		return nil, err
	}
	b.handler = h
	return b, nil
}

// siteURL is the site's address, from the login's return address; empty
// without one.
func siteURL(cfg *config.Config) string {
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
	if old.Connector != new.Connector || old.Secrets.ConnectorToken != new.Secrets.ConnectorToken {
		changed = append(changed, "connector")
	}
	if !bytes.Equal(old.Secrets.Key, new.Secrets.Key) {
		changed = append(changed, "OVERFIN_SECRET_KEY")
	}
	return changed
}
