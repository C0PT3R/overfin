package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
		MoveOnClaim:   cfg.Adoption.Mode == "move",
	}
}

// build makes the request handler for a configuration.
func build(cfg *config.Config, sh *shared) (http.Handler, error) {
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
	if cfg.Adoption.Claim {
		sessions.ClaimMode = cfg.Adoption.Mode
	}
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
		MaxStreams: cfg.Limits.MaxStreamsPerAccount, MaxTranscodes: cfg.Limits.MaxTranscodesTotal,
		RefuseOverLimit: cfg.Limits.OnLimit == "refuse", Logger: logger}
	// Accounts and profiles are always on; everything else is a plugin the
	// operator enables.
	plugins := []plugin.Plugin{
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, Claim: cfg.Adoption.Claim,
			KeepOnDelete: cfg.Accounts.OnDelete == "disable", Images: sh.images, PINs: checker},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: db, MaxProfiles: cfg.Limits.MaxProfilesPerAccount,
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
	logger.Info("plugins", "enabled", cfg.Plugins.Enable)
	return server.New(logger, identityMiddleware, plugins...)
}

// gateway serves every request with the handler built from the current
// configuration, and swaps it when the configuration is reloaded. Requests
// already running finish on the handler they started with.
type gateway struct {
	path string
	sh   *shared

	mu      sync.Mutex // one reload at a time
	cfg     *config.Config
	handler atomic.Pointer[http.Handler]
}

func newGateway(path string, cfg *config.Config, sh *shared) (*gateway, error) {
	h, err := build(cfg, sh)
	if err != nil {
		return nil, err
	}
	g := &gateway{path: path, sh: sh, cfg: cfg}
	g.handler.Store(&h)
	return g, nil
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*g.handler.Load()).ServeHTTP(w, r)
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
	if changed := restartOnly(g.cfg, cfg); len(changed) > 0 {
		return fmt.Errorf("these settings only change with a restart: %s", strings.Join(changed, ", "))
	}
	h, err := build(cfg, g.sh)
	if err != nil {
		return err
	}
	g.handler.Store(&h)
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
	if !bytes.Equal(old.Secrets.Key, new.Secrets.Key) {
		changed = append(changed, "OVERFIN_SECRET_KEY")
	}
	return changed
}
