// Package overfin is Overfin: the server between its apps and Jellyfin,
// with every feature built in. The official build (cmd/overfin) runs it
// with overfin-web, Overfin's own web client, as the site it serves.
package overfin

import (
	"cmp"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/api"
	"github.com/overfin/server/internal/catalog"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/cookie"
	"github.com/overfin/server/internal/emails"
	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/introskipper"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/personal"
	"github.com/overfin/server/internal/playback"
	"github.com/overfin/server/internal/proof"
	"github.com/overfin/server/internal/provision"
	"github.com/overfin/server/internal/ratings"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
	"golang.org/x/crypto/acme/autocert"
)

// builtinSite is the web client this build includes, if any.
var builtinSite fs.FS

// Version is set at build time with
// -ldflags "-X github.com/overfin/server.Version=…".
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
	importFlag := flag.Bool("import-people", false,
		"before moving to managed sign-in: let everyone the identity provider ([connector]) has sign in with their email, keeping their accounts, then exit")
	passwordFor := flag.String("set-password", "",
		"set a new password for this email (managed sign-in), read from the terminal, then exit; for an admin locked out")
	check := flag.Bool("check", false,
		"test the configuration, the secret key, Jellyfin and the identity provider, say what's wrong, then exit; changes nothing")
	flag.Parse()

	// Without a file at the default place, the defaults: a start needs no
	// configuration at all. A file named on purpose must exist.
	if *configPath == defaultPath && !fileExists(defaultPath) {
		*configPath = ""
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

	if *recoverTokens {
		return runRecovery(cfg, db, moduleDBs, k, logger)
	}
	if *importFlag {
		return importPeople(context.Background(), cfg, db, os.Stdout)
	}
	if *passwordFor != "" {
		return setPassword(context.Background(), cfg, db, *passwordFor, os.Stdin, os.Stdout)
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
	var siteHandler http.Handler = site
	if strings.HasPrefix(cfg.Site.URL, "https://") {
		// Its public address says HTTPS: cookies are HTTPS ones, whatever
		// the proxy in front says.
		siteHandler = cookie.AlwaysSecure(site)
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           siteHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: video streams can legitimately run for hours.
	}
	errc := make(chan error, 3)
	// HTTPS by Overfin itself: the site on its own listener, with a
	// certificate from Let's Encrypt; the main one answers Let's Encrypt's
	// checks and sends everyone else to HTTPS.
	var tlsSrv *http.Server
	if h := cfg.HTTPS; h.Enabled() {
		certs := &autocert.Manager{Prompt: autocert.AcceptTOS, HostPolicy: autocert.HostWhitelist(h.Domain),
			Cache: autocert.DirCache(filepath.Join(filepath.Dir(cfg.Storage.Path), "certificates")), Email: h.Email}
		tlsSrv = &http.Server{Addr: h.Listen, Handler: hsts(siteHandler), TLSConfig: certs.TLSConfig(),
			ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		tlsSrv.TLSConfig.MinVersion = tls.VersionTLS12
		// Failed handshakes, Let's Encrypt's refusals among them, in the
		// log like everything else.
		tlsSrv.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelWarn)
		srv.Handler = certs.HTTPHandler(nil)
		go func() {
			logger.Info("listening, HTTPS", "addr", h.Listen, "domain", h.Domain)
			errc <- tlsSrv.ListenAndServeTLS("", "")
		}()
	}
	go func() {
		logger.Info("listening", "addr", addr, "https", tlsSrv != nil)
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
		if tlsSrv != nil {
			_ = tlsSrv.Shutdown(shutdownCtx)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}

	// Jellyfin: Overfin's own (rule 67), at the address the configuration
	// sets or the setup page saved. A fresh one is set up at once; any
	// other waits to be handed over on the setup page, which with managed
	// sign-in asks for the first admin too.
	jellyfinSaved := cfg.Jellyfin.URL == ""
	url, key, haveJellyfin, err := ownJellyfin(ctx, cfg, db, logger)
	if err != nil {
		return err
	}
	fixedURL := cfg.Jellyfin.URL
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	// The Jellyfin settings Overfin needs are its own (rule 67): set back
	// on every start, and as soon as Overfin has its Jellyfin, so the
	// setup wizard's steps (Intro Skipper restarts Jellyfin) run with them.
	if haveJellyfin {
		ownSettings(ctx, db, url, key, logger)
	}
	managed := cfg.Identity.Mode == "managed"
	haveAdmin := true
	if managed {
		if haveAdmin, err = db.HasAdmin(ctx); err != nil {
			return err
		}
	}
	// Intro Skipper's installs: on the Jellyfin the setup wizard has, then
	// on the gateway's.
	skipper := &introskipper.Installer{Logger: logger, Jellyfin: func() (*jellyfin.Client, jellyfin.Auth, bool) {
		if cfg.Jellyfin.URL == "" || cfg.Secrets.JellyfinAdminKey == "" {
			return nil, jellyfin.Auth{}, false
		}
		jf, err := jellyfin.New(cfg.Jellyfin.URL, Version)
		return jf, jellyfin.Auth{Token: cfg.Secrets.JellyfinAdminKey, DeviceID: "overfin-admin"}, err == nil
	}}
	if !haveJellyfin || !haveAdmin {
		if !managed && adminSrv == nil {
			return errors.New("no Jellyfin of Overfin's own yet: switch on the control panel ([admin]), " +
				"and set it up or hand it over on its setup page")
		}
		done := make(chan struct{})
		setup := &admin.Setup{Code: admin.NewSetupCode(), Logger: logger, Done: func() { close(done) }}
		if !haveJellyfin {
			setup.FixedURL = fixedURL
			setup.Jellyfin = func(ctx context.Context, req admin.JellyfinRequest) (admin.JellyfinAnswer, error) {
				url, key, ans, err := takeOver(ctx, db, fixedURL, req, logger)
				if ans.Done {
					cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
					ownSettings(ctx, db, url, key, logger)
				}
				return ans, forPage(err)
			}
		}
		if _, _, set, err := db.Languages(ctx); err != nil {
			return err
		} else if !set {
			setup.Language = func(ctx context.Context, tag string) error {
				_, err := db.SetLanguages(ctx, []string{tag}, tag)
				return err
			}
		}
		// TMDB's token, unless there's one: the step may be skipped, and
		// Overfin then runs on Jellyfin's data.
		if _, ok, err := db.TMDBToken(ctx); err == nil && !ok && cfg.Secrets.TMDBToken == "" {
			setup.TMDB = func(ctx context.Context, token string) (string, error) {
				token = strings.TrimSpace(token)
				if _, err := checkTMDBToken(ctx, db, token); err != nil {
					return "", forPage(err)
				}
				if err := db.SetTMDBToken(ctx, token); err != nil {
					return "", err
				}
				return "TMDB takes the token.", nil
			}
		}
		// Seerr, unless it's connected: the step may be skipped.
		if _, _, ok, err := db.Seerr(ctx); err == nil && !ok {
			setup.Seerr = func(ctx context.Context, url, key string) (string, error) {
				url = strings.TrimRight(strings.TrimSpace(url), "/")
				key = strings.TrimSpace(key)
				_, notes, err := checkSeerr(ctx, url, key)
				if err != nil {
					return "", forPage(err)
				}
				if err := db.SetSeerr(ctx, url, key); err != nil {
					return "", err
				}
				return "Seerr's API key works." + notes, nil
			}
		}
		// Intro Skipper, unless it's there.
		setup.IntroSkipper = skipper
		if haveJellyfin {
			if st, err := skipper.Look(ctx); err != nil || st.Installed {
				setup.IntroSkipper = nil
			}
		}
		if !haveAdmin {
			setup.FirstAdmin = func(ctx context.Context, email, hash string) error {
				u, err := db.CreateUser(ctx, email, true)
				if err != nil {
					return err
				}
				return db.SetPassword(ctx, u.Subject, hash)
			}
		}
		where := []any{"code", setup.Code}
		if adminSrv != nil {
			panel.set(setup.Handler())
			where = append(where, "panel", cfg.Admin.Listen)
		}
		if managed && adminSrv == nil {
			// admin.private keeps the setup page to private addresses too,
			// like the panel it comes before.
			if cfg.Admin.Private {
				site.set(setup.PrivateSiteHandler())
			} else {
				site.set(setup.PublicHandler())
			}
			where = append(where, "site", cmp.Or(cfg.Site.URL, addr))
		}
		logger.Warn("Overfin isn't set up yet: open its address (/admin/) and finish setting it up with this one-time code", where...)
		select {
		case <-done:
		case err := <-errc:
			return err
		case <-ctx.Done():
			return shutdown()
		}
		site.set(settingUp)
	}

	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	sh.jellyfinSaved = jellyfinSaved
	// Email, as the panel set it, unless the configuration file does.
	if !cfg.Mail.Enabled() {
		if raw, pw, ok, err := db.Mail(ctx); err != nil {
			logger.Warn("email's settings can't be read: set email up again in the control panel", "err", err)
		} else if ok {
			var m config.Mail
			if err := json.Unmarshal(raw, &m); err != nil {
				logger.Warn("email's settings can't be read: set email up again in the control panel", "err", err)
			} else {
				sh.mail.Store(senderFor(m, pw, db))
			}
		}
	}
	// Seerr, if the panel connected it. Settings sealed with a lost secret
	// key can't be read: Overfin starts without it, and says so.
	if url, key, ok, err := db.Seerr(ctx); err != nil {
		logger.Warn("Seerr's settings can't be read: connect Seerr again in the control panel", "err", err)
	} else if ok {
		if c, err := seerr.New(url, key); err == nil {
			sh.seerr, sh.seerrURL, sh.seerrKey = c, url, key
		}
	}
	// TMDB is asked in the default language's regional tag; an install
	// from before asked in two letters, and fetches its titles again once.
	if changed, err := db.SyncTMDBLanguage(ctx); err != nil {
		logger.Warn("TMDB's language couldn't be brought in line", "err", err)
	} else if changed {
		logger.Info("TMDB is asked in the default language's region now: titles are fetched again")
	}
	// TMDB itself, with the operator's own key, in the default language.
	// Without one, Overfin runs on Jellyfin's data (and Seerr's, when it's
	// connected).
	tmdbToken := cfg.Secrets.TMDBToken
	if tmdbToken == "" {
		if k, ok, err := db.TMDBToken(ctx); err != nil {
			logger.Warn("TMDB's token can't be read: give it again in the control panel", "err", err)
		} else if ok {
			tmdbToken = k
		}
	}
	if tmdbToken != "" {
		if c, err := tmdbClient(db, tmdbToken); err == nil {
			sh.tmdb = c
		} else {
			logger.Warn("TMDB's token isn't a read access token: give it again in the control panel")
		}
	} else {
		logger.Warn("no TMDB token: Overfin runs on Jellyfin's data; give one in the control panel (Server, TMDB)")
	}
	// What TMDB says about the library's titles, kept in the background
	// (started below) and fetched on demand by the site's pages, which is
	// why it exists before them. Its source is the gateway's, once there is
	// one.
	var gw *gateway
	sh.texts = catalog.NewTexts(func(language string) catalog.TitleSource { return gw.textSource(language) }, db, logger,
		100*time.Millisecond, 3*time.Second)
	sh.titles = catalog.NewTitles(func() catalog.TitleSource { return gw.titleSource() }, db, logger,
		10*time.Minute, 100*time.Millisecond)
	gw, err = newGateway(*configPath, cfg, sh)
	if err != nil {
		return err
	}
	skipper.Jellyfin = func() (*jellyfin.Client, jellyfin.Auth, bool) { return sh.jf, gw.adminAuth(), true }
	// The service account follows the configuration's admin key.
	sh.service.Provisioner = gw.Provisioner
	site.set(gw)

	// Playbacks outlive reloads, so their janitor runs for the whole run:
	// it needs only the registry and Jellyfin.
	janitor := api.Playback{Library: api.Library{Jellyfin: sh.jf, Logger: logger}, Tickets: sh.tickets, Logger: logger}
	go janitor.RunJanitor(ctx)
	go purgeExpired(ctx, db, logger)
	// The catalog's index, read through Overfin's Jellyfin service account,
	// as Jellyfin's socket says the library changed (polling only while
	// it's out of reach), and whole once a day.
	cat := &catalog.Sync{Jellyfin: sh.jf, Store: db, Logger: logger,
		Service:      sh.service,
		ChangesEvery: 5 * time.Minute, FullEvery: 24 * time.Hour}
	go cat.Run(ctx)
	go cat.Watch(ctx)
	// Discovery's lists, from TMDB: 10 pages (200 titles) per list, every
	// hour.
	sh.lists = catalog.NewLists(gw.tmdbSource, db, logger, time.Hour, 10)
	go sh.lists.Run(ctx)
	// Titles' certifications, for kids profiles: looked for every 10
	// minutes, a tenth of a second apart.
	sh.ratings = ratings.NewKeeper(gw.ratingSource, db, logger, 10*time.Minute, 100*time.Millisecond)
	go sh.ratings.Run(ctx)
	// The library's titles from TMDB: looked for every 10 minutes, a tenth
	// of a second between requests.
	go sh.titles.Run(ctx)
	go sh.texts.Run(ctx)
	go unrestrictKids(ctx, db, gw.Provisioner, logger)
	// Each profile's data, brought over from Jellyfin once.
	// With Pocket ID, Overfin sets up its own client and groups there.
	if cn := cfg.Connector; cn.Kind == "pocketid" && cfg.OIDC.Enabled() {
		go setUpPocketID(ctx, cfg, logger)
	}
	// The library's artwork, kept on disk next to the database.
	go (&catalog.LibraryArtwork{Store: db, Images: sh.images, Logger: logger, Every: time.Hour}).Run(ctx)
	go (&personal.Importer{Jellyfin: sh.jf, Store: db, Logger: logger, Every: 10 * time.Minute}).Run(ctx)

	if a := cfg.Admin; a.Enabled() || managed {
		o := cfg.OIDC
		p := &admin.Panel{Features: gw, Modules: gw, Jellyfin: gw, Libraries: libraries{gw}, Transcoding: gw, Seerr: gw, Catalog: cat, Group: a.Group, Logger: logger,
			TMDB: func() bool { return gw.tmdbSource() != nil }, TMDBTokens: gw, IntroSkipper: skipper,
			// A new language: artwork and discovery's lists, read again now.
			OnLanguage: func() { sh.titles.Wake(); sh.lists.Wake() },
			Store:      db, Provisioner: newProvisioner(cfg, sh),
			Provider: &oidc.Provider{Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: cfg.Secrets.OIDCClientSecret,
				RedirectURL: a.RedirectURL, GroupsClaim: o.GroupsClaim, HTTP: oidc.NewClient(o.Dial)}}
		if sh.directory != nil {
			p.Directory = sh.directory
		}
		// Identification: TMDB searched as the library's titles are
		// described (TMDB itself); a title fixed by hand is fetched at once.
		p.SearchTMDB = func(ctx context.Context, query string) ([]tmdb.Title, error) {
			src := gw.titleSource()
			if src == nil {
				return nil, tmdb.ErrNotFound
			}
			titles, _, err := src.Search(ctx, query)
			return titles, err
		}
		p.Images = sh.images
		p.OnRetitled = sh.titles.Wake
		switch {
		case managed && a.Enabled():
			// Only on its own listener, private, which serves the site's
			// sign-in too: admins sign in there, on that address.
			p.Provider, p.SiteAdmin, p.Directory, p.People, p.Mail = nil, gw.SiteAdmin, admin.Users{Store: db}, gw, gw
			panel.set(admin.PrivateOnly(withPanel(gw, p.SiteHandler(false))))
		case managed:
			// At /admin/ on the site, for its admins.
			p.Provider, p.SiteAdmin, p.Directory, p.People, p.Mail = nil, gw.SiteAdmin, admin.Users{Store: db}, gw, gw
			site.set(withPanel(gw, p.SiteHandler(a.Private)))
		default:
			panel.set(p.Handler())
		}
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
	art := images.New(jf, 64<<20)
	art.ArtworkDir = filepath.Join(filepath.Dir(cfg.Storage.Path), "artwork")
	sh := &shared{logger: logger, db: db, moduleDBs: moduleDBs, keys: k, jf: jf,
		images: art, tickets: playback.NewTickets(24 * time.Hour), folders: api.NewFolderCache(),
		searches: &api.AccountLimiter{PerMinute: 30}, requests: &api.AccountLimiter{PerMinute: 20}, requested: &api.SeerrRequested{Logger: logger},
		service: &provision.Service{},
		// Emails, with the operator's own templates next to the database.
		emails: &emails.Writer{Dir: filepath.Join(filepath.Dir(cfg.Storage.Path), "mail-templates"), Store: db, Logger: logger},
		proof:  &proof.Checker{Key: k.Proof}}
	if cn := cfg.Connector; cn.Enabled() {
		sh.directory = &connector.Cache{Directory: connector.New(cn.Kind, cn.URL, cfg.Secrets.ConnectorToken, oidc.NewClient(cn.Dial))}
	}
	return sh, nil
}

// runRecovery runs -recover-tokens. It needs Overfin's Jellyfin: Overfin's
// key for it was sealed with the lost secret key too, so the operator hands
// it over again, in the terminal (SECURITY.md rule 67).
func runRecovery(cfg *config.Config, db *store.Store, moduleDBs map[string]*sql.DB, k *keys.Keys, logger *slog.Logger) error {
	ctx := context.Background()
	url, key, saved, err := db.Jellyfin(ctx)
	switch {
	case err != nil:
		if url, key, err = handOverInTerminal(ctx, db, cfg.Jellyfin.URL, os.Stdin, os.Stderr); err != nil {
			return err
		}
	case !saved:
		return errors.New("nothing to recover: no Jellyfin set up yet")
	}
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = url, key
	sh, err := newShared(cfg, db, moduleDBs, k, logger)
	if err != nil {
		return err
	}
	return recoverTokensWith(ctx, logger, db, newProvisioner(cfg, sh), k.Check)
}

// hsts tells browsers to come back over HTTPS only, for a year: on the
// site Overfin serves HTTPS for itself. Its other subdomains are the
// operator's business.
func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// withPanel serves the control panel at /admin/ (and what apps read, at
// /apps/) beside the site.
func withPanel(site, panel http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/admin/", panel)
	mux.Handle("/apps/", panel)
	mux.Handle("/", site)
	return mux
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
		if err := db.DeleteOldEmailLinks(ctx); err != nil && ctx.Err() == nil {
			logger.Error("purging old links failed", "err", err)
		}
		// Those who registered and never confirmed their address go.
		if n, err := db.DeleteUnconfirmed(ctx, time.Now().Add(-api.UnconfirmedFor)); err != nil && ctx.Err() == nil {
			logger.Error("removing unconfirmed registrations failed", "err", err)
		} else if n > 0 {
			logger.Info("unconfirmed registrations removed", "people", n)
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

// unrestrictKids lifts Jellyfin's rating limits from kids profiles' users,
// set by Overfin before it had its own ratings: Overfin applies those now.
// What fails is tried again ten minutes later.
func unrestrictKids(ctx context.Context, db *store.Store, prov func() *provision.Provisioner, logger *slog.Logger) {
	for {
		kids, err := db.KidsProfiles(ctx)
		failed := err != nil
		for _, p := range kids {
			if p.JellyfinUserID == "" {
				continue // none of its own: nothing to lift
			}
			err = prov().Unrestrict(ctx, p.JellyfinUserID)
			if jellyfin.IsStatus(err, http.StatusNotFound) {
				err = nil // not on this server: nothing to lift
			}
			if err != nil {
				failed = true
			}
		}
		if !failed {
			return
		}
		logger.Warn("could not lift Jellyfin's limits from kids profiles yet", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
	}
}

// setUpPocketID makes sure Pocket ID has Overfin's client (its ID, secret
// and return addresses: the site's and the control panel's) and the groups
// the configuration names, trying again until Pocket ID answers.
func setUpPocketID(ctx context.Context, cfg *config.Config, logger *slog.Logger) {
	cn, o := cfg.Connector, cfg.OIDC
	p := &connector.PocketID{BaseURL: cn.URL, Key: cfg.Secrets.ConnectorToken, HTTP: oidc.NewClient(cn.Dial)}
	client := connector.Client{ID: o.ClientID, Name: "Overfin", Secret: cfg.Secrets.OIDCClientSecret, LaunchURL: siteURL(cfg)}
	for _, u := range []string{o.RedirectURL, cfg.Admin.RedirectURL} {
		if u != "" {
			client.CallbackURLs = append(client.CallbackURLs, u)
		}
	}
	var groups []string
	for _, g := range []string{cfg.Admin.Group, cfg.Identity.RequiredGroup} {
		if g != "" && !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	for wait := 5 * time.Second; ; wait = min(2*wait, 5*time.Minute) {
		err := p.Setup(ctx, client, groups)
		if err == nil {
			logger.Info("Pocket ID is set up for Overfin", "client", client.ID, "groups", groups)
			if email := cn.FirstAdmin; email != "" {
				if made, err := p.FirstAdmin(ctx, email, groups); err != nil {
					logger.Error("making Pocket ID's first admin failed", "err", err)
				} else if made {
					logger.Info("Pocket ID's first admin is made; Pocket ID's own command gives them a sign-in link", "email", email,
						"command", "pocket-id one-time-access-token "+email)
				}
			}
			if s, err := p.Signups(ctx); err == nil && s == "disabled" {
				logger.Warn("Pocket ID's sign-ups are disabled, so invitation links can't work: set ALLOW_USER_SIGNUPS=withToken there")
			}
			return
		}
		logger.Warn("setting up Pocket ID failed; trying again", "err", err, "in", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
