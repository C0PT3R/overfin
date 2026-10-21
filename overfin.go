// Package overfin is Overfin: the server between its apps and Jellyfin,
// with every feature built in. The official build (cmd/overfin) runs it
// with overfin-web, Overfin's own web client, as the site it serves.
package overfin

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/C0PT3R/overfin-server/internal/admin"
	"github.com/C0PT3R/overfin-server/internal/api"
	"github.com/C0PT3R/overfin-server/internal/catalog"
	"github.com/C0PT3R/overfin-server/internal/config"
	"github.com/C0PT3R/overfin-server/internal/connector"
	"github.com/C0PT3R/overfin-server/internal/images"
	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/keys"
	"github.com/C0PT3R/overfin-server/internal/oidc"
	"github.com/C0PT3R/overfin-server/internal/personal"
	"github.com/C0PT3R/overfin-server/internal/playback"
	"github.com/C0PT3R/overfin-server/internal/provision"
	"github.com/C0PT3R/overfin-server/internal/seal"
	"github.com/C0PT3R/overfin-server/internal/seerr"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// builtinSite is the web client this build includes, if any.
var builtinSite fs.FS

// Version is set at build time with
// -ldflags "-X github.com/C0PT3R/overfin-server.Version=…".
var Version = "dev"

// Run runs Overfin, serving site as its web client when the configuration
// leaves [site] serve at "builtin" (nil for none), and exits when it stops.
// Command-line flags are parsed here.
func Run(site fs.FS) {
	builtinSite = site
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); errors.Is(err, errCheckFailed) {
		os.Exit(1) // what's wrong is already said, in plain words
	} else if err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// firstKey generates the secret key when none was given and none was
// generated yet, for a database that doesn't exist yet only. An existing
// database without its key is a lost key, not a first start: a new key
// couldn't read it, so Overfin stops, unless it's recovering from exactly
// that (-recover-tokens).
func firstKey(cfg *config.Config, recovering bool, logger *slog.Logger) error {
	_, err := os.Stat(cfg.Storage.Path)
	if !errors.Is(err, fs.ErrNotExist) && !recovering {
		return fmt.Errorf("no secret key: OVERFIN_SECRET_KEY isn't set and %s doesn't exist, but the database "+
			"does. Restore the key from your backup; if it's lost for good, run overfin -recover-tokens once "+
			"to make a new one (profiles keep their history; PINs and logins are reset)", config.KeyFile)
	}
	key, err := config.GenerateKey()
	if err != nil {
		return err
	}
	cfg.Secrets.Key = key
	logger.Warn("generated a new secret key: back it up, apart from the database", "file", config.KeyFile)
	return nil
}

func run(logger *slog.Logger) error {
	// Everything Overfin creates (database, cache) is readable by its own
	// user only.
	syscall.Umask(0o077)

	defaultPath := os.Getenv("OVERFIN_CONFIG")
	if defaultPath == "" {
		defaultPath = "/etc/overfin/overfin.toml"
	}
	configPath := flag.String("config", defaultPath, "path to the configuration file")
	recoverTokens := flag.Bool("recover-tokens", false,
		"after the secret key was lost: give every profile a fresh Jellyfin token sealed with the current key, then exit")
	check := flag.Bool("check", false,
		"test the configuration, the secret key, Jellyfin and the identity provider, say what's wrong, then exit; changes nothing")
	flag.Parse()

	if *check {
		return checkSetup(*configPath, os.Getenv, os.Stdout)
	}
	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	addr := cfg.Listen
	if cfg.Secrets.Key == nil {
		if err := firstKey(cfg, *recoverTokens, logger); err != nil {
			return err
		}
	}

	k, err := keys.Derive(cfg.Secrets.Key)
	if err != nil {
		return err
	}
	box, err := seal.New(k.TokenEncryption)
	if err != nil {
		return err
	}
	db, err := store.Open(context.Background(), cfg.Storage.Path, box)
	if err != nil {
		return err
	}
	defer db.Close()
	logger.Info("database ready", "path", cfg.Storage.Path)
	moduleDBs, err := openModules(context.Background(), db, cfg.Storage.Path, logger)
	if err != nil {
		return err
	}
	defer closeModules(moduleDBs)
	if !*recoverTokens {
		if err := db.CheckKey(context.Background(), k.Check); errors.Is(err, store.ErrWrongKey) {
			return fmt.Errorf("%w. Restore the original key; if it's lost for good, run overfin -recover-tokens once "+
				"with the new key (profiles keep their history; PINs and logins are reset)", err)
		} else if err != nil {
			return err
		}
	}

	if *recoverTokens {
		return runRecovery(cfg, db, moduleDBs, k, logger)
	}

	// Stop cleanly on Ctrl+C or `docker stop`; reload the configuration on
	// SIGHUP (`docker kill -s HUP …`).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	// The listeners start now, before Overfin is ready, so the setup page can
	// come first: until then, the site says it's being set up.
	site, panel := newSwap(settingUp), newSwap(http.NotFoundHandler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           site,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: video streams can legitimately run for hours.
	}
	errc := make(chan error, 2)
	go func() {
		logger.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	// The control panel, on its own listener (rule 58).
	var adminSrv *http.Server
	if a := cfg.Admin; a.Enabled() {
		adminSrv = &http.Server{Addr: a.Listen, Handler: panel,
			ReadHeaderTimeout: 10 * time.Second, WriteTimeout: time.Minute, IdleTimeout: 2 * time.Minute}
		go func() {
			logger.Info("control panel listening", "addr", a.Listen, "https", a.TLSCert != "")
			if a.TLSCert != "" {
				errc <- adminSrv.ListenAndServeTLS(a.TLSCert, a.TLSKey)
			} else {
				errc <- adminSrv.ListenAndServe()
			}
		}()
	}
	shutdown := func() error {
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if adminSrv != nil {
			_ = adminSrv.Shutdown(shutdownCtx)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	// Jellyfin: set in the configuration, saved by the setup page, or asked
	// for now.
	jellyfinSaved := cfg.Jellyfin.URL == ""
	if jellyfinSaved {
		url, key, ok, err := db.Jellyfin(ctx)
		if err != nil {
			return err
		}
		if !ok {
			if adminSrv == nil {
				return errors.New("no Jellyfin set up: set jellyfin.url and OVERFIN_JELLYFIN_ADMIN_KEY, " +
					"or switch on the control panel ([admin]) and use its setup page")
			}
			saved := make(chan [2]string, 1)
			// The page tests, then saves, one request at a time: the save
			// keeps the server ID its test just found.
			var serverID string
			setup := &admin.Setup{Code: admin.NewSetupCode(), Logger: logger,
				Test: func(ctx context.Context, url, key string) (string, error) {
					found, err := testJellyfin(ctx, url, key)
					serverID = found.serverID
					return found.summary, err
				},
				Save: func(ctx context.Context, url, key string) error {
					if err := db.SetJellyfin(ctx, url, key, serverID); err != nil {
						return err
					}
					saved <- [2]string{url, key}
					return nil
				}}
			panel.set(setup.Handler())
			logger.Warn("Overfin isn't set up yet: open the control panel's address to connect Jellyfin, with this one-time code",
				"code", setup.Code, "panel", cfg.Admin.Listen)
			select {
			case v := <-saved:
				url, key = v[0], v[1]
			case err := <-errc:
				return err
			case <-ctx.Done():
				return shutdown()
			}
		}
		cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	}

	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	sh.jellyfinSaved = jellyfinSaved
	// Seerr, if the panel connected it. Settings sealed with a lost secret
	// key can't be read: Overfin starts without it, and says so.
	if url, key, ok, err := db.Seerr(ctx); err != nil {
		logger.Warn("Seerr's settings can't be read: connect Seerr again in the control panel", "err", err)
	} else if ok {
		if c, err := seerr.New(url, key); err == nil {
			sh.seerr, sh.seerrURL, sh.seerrKey = c, url, key
		}
	}
	gw, err := newGateway(*configPath, cfg, sh)
	if err != nil {
		return err
	}
	site.set(gw)

	// Playbacks outlive reloads, so their janitor runs for the whole run:
	// it needs only the registry and Jellyfin.
	janitor := api.Playback{Library: api.Library{Jellyfin: sh.jf, Logger: logger}, Tickets: sh.tickets, Logger: logger}
	go janitor.RunJanitor(ctx)
	go purgeExpired(ctx, db, logger)
	// The catalog's index, read through Overfin's Jellyfin service account.
	cat := &catalog.Sync{Jellyfin: sh.jf, Store: db, Logger: logger,
		Service:      &provision.Service{Provisioner: gw.Provisioner},
		ChangesEvery: 5 * time.Minute, FullEvery: 6 * time.Hour}
	go cat.Run(ctx)
	// Discovery's lists, from Seerr when it's connected: 10 pages (200
	// titles) per list, every hour.
	sh.lists = catalog.NewLists(gw.currentSeerr, db, logger, time.Hour, 10)
	go sh.lists.Run(ctx)
	// Each profile's data, brought over from Jellyfin once.
	go (&personal.Importer{Jellyfin: sh.jf, Store: db, Logger: logger, Every: 10 * time.Minute}).Run(ctx)

	if a := cfg.Admin; a.Enabled() {
		o := cfg.OIDC
		p := &admin.Panel{Features: gw, Modules: gw, Jellyfin: gw, Seerr: gw, Catalog: cat, Group: a.Group, Logger: logger,
			Store: db, Provisioner: newProvisioner(cfg, sh),
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: a.RedirectURL, GroupsClaim: o.GroupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		if cn := cfg.Connector; cn.Enabled() {
			p.Directory = &connector.Authentik{BaseURL: cn.URL, Token: cfg.Secrets.ConnectorToken, HTTP: oidc.NewClient(cn.Dial)}
		}
		panel.set(p.Handler())
	}

	for {
		select {
		case err := <-errc:
			return err
		case <-hup:
			if err := gw.reload(os.Getenv); err != nil {
				logger.Error("configuration not reloaded; the running one stays", "err", err)
			} else {
				logger.Info("configuration reloaded")
			}
		case <-ctx.Done():
			return shutdown()
		}
	}
}

// newShared makes what lives for the whole run, once Jellyfin is known.
func newShared(cfg *config.Config, db *store.Store, moduleDBs map[string]*sql.DB, k *keys.Keys, logger *slog.Logger) (*shared, error) {
	jf, err := jellyfin.New(cfg.Jellyfin.URL, Version)
	if err != nil {
		return nil, err
	}
	return &shared{logger: logger, db: db, moduleDBs: moduleDBs, keys: k, jf: jf,
		images: images.New(jf, 64<<20), tickets: playback.NewTickets(24 * time.Hour)}, nil
}

// runRecovery runs -recover-tokens. It needs Jellyfin: from the
// configuration, or as the setup page saved it. Saved settings sealed with
// the lost key can't be read, so then the configuration must give them, and
// they're saved again with the new key.
func runRecovery(cfg *config.Config, db *store.Store, moduleDBs map[string]*sql.DB, k *keys.Keys, logger *slog.Logger) error {
	ctx := context.Background()
	url, key, saved, err := db.Jellyfin(ctx)
	switch {
	case cfg.Jellyfin.URL != "":
		if err != nil {
			id, err := db.JellyfinServer(ctx)
			if err != nil {
				return err
			}
			if err := db.SetJellyfin(ctx, cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey, id); err != nil {
				return err
			}
		}
	case err != nil:
		return fmt.Errorf("%w. For the recovery, set jellyfin.url and OVERFIN_JELLYFIN_ADMIN_KEY", err)
	case !saved:
		return errors.New("nothing to recover: no Jellyfin set up yet")
	default:
		cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	}
	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	return recoverTokensWith(ctx, logger, db, newProvisioner(cfg, sh), k.Check)
}

// settingUp is what the site answers until Overfin is set up.
var settingUp = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "30")
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "This site is being set up. Come back in a moment.", http.StatusServiceUnavailable)
})

// swap serves whichever handler it holds, which can change while serving:
// a notice or the setup page first, then Overfin itself.
type swap struct{ h atomic.Pointer[http.Handler] }

func newSwap(h http.Handler) *swap {
	s := &swap{}
	s.set(h)
	return s
}

func (s *swap) set(h http.Handler) { s.h.Store(&h) }

func (s *swap) ServeHTTP(w http.ResponseWriter, r *http.Request) { (*s.h.Load()).ServeHTTP(w, r) }

// purgeExpired deletes expired sessions and logins every hour. They are
// already refused when expired; this only keeps the database small.
func purgeExpired(ctx context.Context, db *store.Store, logger *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if _, err := db.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging expired sessions failed", "err", err)
		}
		if _, err := db.DeleteExpiredLogins(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging expired logins failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recoverTokensWith gives every profile whose Jellyfin token can't be read (sealed
// with a lost key) a fresh one sealed with the current key, then makes the
// current key the database's key. Profiles keep their Jellyfin users, so
// their history. PINs and logins, tied to the old key, are reset.
func recoverTokensWith(ctx context.Context, logger *slog.Logger, db *store.Store, prov *provision.Provisioner, check []byte) error {
	broken, err := db.UnreadableTokens(ctx)
	if err != nil {
		return err
	}
	logger.Info("recovery: profiles with unreadable tokens", "count", len(broken))
	failed := 0
	for _, u := range broken {
		token, err := prov.Relogin(ctx, u.JellyfinUserID, u.JellyfinUsername)
		if err == nil {
			err = db.SetJellyfinToken(ctx, u.ProfileID, token)
		}
		if err != nil {
			failed++
			logger.Error("recovery: profile not recovered", "profile", u.ProfileID, "err", err)
			continue
		}
		logger.Info("recovery: profile recovered", "profile", u.ProfileID)
	}
	if failed > 0 {
		return fmt.Errorf("recovery: %d profiles not recovered; the key stays unaccepted, fix and run again", failed)
	}
	pins, logins, err := db.ForgetSealed(ctx)
	if err != nil {
		return err
	}
	if err := db.SetKeyCheck(ctx, check); err != nil {
		return err
	}
	logger.Info("recovery: done; PINs cleared and everyone logs in again", "pins", pins, "logins", logins)
	return nil
}
