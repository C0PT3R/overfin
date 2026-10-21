// Package overfin is Overfin's core: the gateway, ready to run with the
// modules it's given.
//
// The official build (cmd/overfin) is Main with the official modules. An
// operator's own build adds theirs:
//
//	package main
//
//	import (
//		"github.com/C0PT3R/overfin-gateway"
//		"github.com/C0PT3R/overfin-gateway/modules/watchlist"
//		"example.org/mymodule"
//	)
//
//	func main() { overfin.Main(watchlist.Module{}, mymodule.Module{}) }
//
// See the module package for writing one.
package overfin

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/admin"
	"github.com/C0PT3R/overfin-gateway/internal/api"
	"github.com/C0PT3R/overfin-gateway/internal/config"
	"github.com/C0PT3R/overfin-gateway/internal/connector"
	"github.com/C0PT3R/overfin-gateway/internal/images"
	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/keys"
	"github.com/C0PT3R/overfin-gateway/internal/oidc"
	"github.com/C0PT3R/overfin-gateway/internal/playback"
	"github.com/C0PT3R/overfin-gateway/internal/provision"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
	"github.com/C0PT3R/overfin-gateway/internal/store"
	"github.com/C0PT3R/overfin-gateway/module"
)

// builtinSite is the web client this build includes, if any.
var builtinSite fs.FS

// Version is set at build time with
// -ldflags "-X github.com/C0PT3R/overfin-gateway.Version=…".
var Version = "dev"

// Main runs Overfin with the given modules compiled in, and no site of its
// own, and exits when it stops. Command-line flags are parsed here.
func Main(mods ...module.Module) {
	Run(Build{Modules: mods})
}

// Build is what a build of Overfin includes besides its core, the gateway.
type Build struct {
	// Modules are compiled in, each switched on or off in the control panel.
	Modules []module.Module
	// Site is the web client this build serves at the site's root, when the
	// configuration leaves [site] serve at "builtin"; nil for none. The
	// official build includes overfin-web.
	Site fs.FS
}

// Run runs Overfin as b describes, and exits when it stops. Command-line
// flags are parsed here.
func Run(b Build) {
	modules, builtinSite = b.Modules, b.Site
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

	if err := registerModules(); err != nil {
		return err
	}
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

	jf, err := jellyfin.New(cfg.Jellyfin.URL, Version)
	if err != nil {
		return err
	}
	sh := &shared{logger: logger, db: db, moduleDBs: moduleDBs, keys: k, jf: jf,
		images: images.New(jf, 64<<20), tickets: playback.NewTickets(24 * time.Hour)}

	if *recoverTokens {
		return recoverTokensWith(context.Background(), logger, db, newProvisioner(cfg, sh), k.Check)
	}

	gw, err := newGateway(*configPath, cfg, sh)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           gw,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: video streams can legitimately run for hours.
	}

	// Stop cleanly on Ctrl+C or `docker stop`; reload the configuration on
	// SIGHUP (`docker kill -s HUP …`).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	// Playbacks outlive reloads, so their janitor runs for the whole run:
	// it needs only the registry and Jellyfin.
	janitor := api.Playback{Library: api.Library{Jellyfin: jf, Logger: logger}, Tickets: sh.tickets, Logger: logger}
	go janitor.RunJanitor(ctx)
	go purgeExpired(ctx, db, logger)

	errc := make(chan error, 2)
	go func() {
		logger.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	// The control panel, on its own listener (rule 58).
	var adminSrv *http.Server
	if a := cfg.Admin; a.Enabled() {
		o := cfg.OIDC
		panel := &admin.Panel{Features: gw, Modules: gw, Group: a.Group, Logger: logger,
			Store: db, Provisioner: newProvisioner(cfg, sh),
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: a.RedirectURL, GroupsClaim: o.GroupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		if cn := cfg.Connector; cn.Enabled() {
			panel.Directory = &connector.Authentik{BaseURL: cn.URL, Token: cfg.Secrets.ConnectorToken, HTTP: oidc.NewClient(cn.Dial)}
		}
		adminSrv = &http.Server{Addr: a.Listen, Handler: panel.Handler(),
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

	for running := true; running; {
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
			running = false
		}
	}

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
