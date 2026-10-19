// Command overfin runs the Overfin gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/C0PT3R/overfin/internal/api"
	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/images"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/keys"
	"github.com/C0PT3R/overfin/internal/pin"
	"github.com/C0PT3R/overfin/internal/playback"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/server"
	"github.com/C0PT3R/overfin/internal/store"
)

// version is set at build time with -ldflags "-X main.version=…".
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
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
	flag.Parse()

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}
	addr := cfg.Listen

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

	var ids *identity.Resolver
	switch cfg.Identity.Mode {
	case "forwardauth":
		ids = identity.NewForwardAuth(cfg.Identity.Proxies, cfg.Identity.UserHeader, cfg.Identity.GroupsHeader, cfg.Identity.GroupsSeparator)
	case "anonymous":
		ids = identity.NewAnonymous(cfg.Identity.Proxies)
	default:
		return fmt.Errorf("identity mode %q is not supported", cfg.Identity.Mode)
	}

	jf, err := jellyfin.New(cfg.Jellyfin.URL, version)
	if err != nil {
		return err
	}
	prov := &provision.Provisioner{
		Store:         db,
		Jellyfin:      jf,
		AdminKey:      cfg.Secrets.JellyfinAdminKey,
		RequiredGroup: cfg.Identity.RequiredGroup,
		LogKey:        k.LogID,
		Logger:        logger,
	}

	sessions := &api.Sessions{Provisioner: prov, Store: db, Logger: logger, Lifetime: 30 * 24 * time.Hour}
	imgs := images.New(jf, 64<<20)
	lib := api.Library{Sessions: sessions, Provisioner: prov, Jellyfin: jf, Images: imgs, CursorKey: k.Cursor, Logger: logger}
	pb := api.Playback{Library: lib, Tickets: playback.NewTickets(24 * time.Hour),
		MaxBitrateKbps: cfg.Limits.MaxBitrateKbps, VersionNames: cfg.Privacy.Exposes("version.names"),
		MaxStreams: cfg.Limits.MaxStreamsPerAccount, MaxTranscodes: cfg.Limits.MaxTranscodesTotal,
		RefuseOverLimit: cfg.Limits.OnLimit == "refuse", Logger: logger}
	// Accounts and profiles are always on; everything else is a plugin the
	// operator enables.
	plugins := []plugin.Plugin{
		api.Account{Sessions: sessions, Provisioner: prov, Logger: logger, Claim: cfg.Adoption.Claim},
		api.Profiles{Sessions: sessions, Provisioner: prov, Store: db, MaxProfiles: cfg.Limits.MaxProfilesPerAccount,
			PINs: &pin.Checker{Key: k.PIN, Store: db}, Images: imgs,
			LogoutURL: cfg.Identity.LogoutURL, Logger: logger},
	}
	if cfg.Plugins.Enabled("library") {
		plugins = append(plugins, lib)
	}
	if cfg.Plugins.Enabled("playback") {
		plugins = append(plugins, pb, api.Stream{Playback: pb})
	}
	logger.Info("plugins", "enabled", cfg.Plugins.Enable)
	handler, err := server.New(logger, ids, plugins...)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: video streams can legitimately run for hours.
	}

	// Stop cleanly on Ctrl+C or `docker stop`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Plugins.Enabled("playback") {
		go pb.RunJanitor(ctx)
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
