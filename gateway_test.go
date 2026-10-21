package overfin

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/config"
	"github.com/C0PT3R/overfin-gateway/internal/images"
	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/keys"
	"github.com/C0PT3R/overfin-gateway/internal/playback"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

const baseConfig = `
listen = ":8080"
[jellyfin]
url = "http://192.0.2.10:8096"
[identity]
mode = "forwardauth"
trusted_proxies = ["127.0.0.1"]
user_header = "Remote-User"
[storage]
path = "%DB%"
`

func env(key string) func(string) string {
	vars := map[string]string{
		"OVERFIN_JELLYFIN_ADMIN_KEY": "admin",
		"OVERFIN_SECRET_KEY":         base64.StdEncoding.EncodeToString([]byte(strings.Repeat(key, 32))),
	}
	return func(name string) string { return vars[name] }
}

// switchedOn tells whether a feature is on, from its control panel tab.
func switchedOn(g *gateway, name string) bool {
	for _, tab := range g.Tabs() {
		if tab.Name == name {
			return true
		}
	}
	return false
}

// status asks the gateway for path, as a logged-out visitor.
func status(g http.Handler, path string) int {
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code
}

func TestReload(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	write := func(extra string) {
		t.Helper()
		content := strings.ReplaceAll(baseConfig, "%DB%", dbPath) + extra
		if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("")
	cfg, err := config.Load(cfgPath, env("a"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jf, _ := jellyfin.New(cfg.Jellyfin.URL, "test")
	sh := &shared{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), db: db, keys: k, jf: jf,
		images: images.New(jf, 1<<20), tickets: playback.NewTickets(time.Hour)}

	g, err := newGateway(cfgPath, cfg, sh)
	if err != nil {
		t.Fatal(err)
	}
	// The library is always on; invites are off until switched on.
	if code := status(g, "/api/v1/libraries"); code != 401 {
		t.Fatalf("library: %d", code)
	}
	if switchedOn(g, "invites") {
		t.Fatal("invites on before switching them on")
	}

	// The panel switches invites on: it applies at once, and is saved.
	f := g.Features()
	f.Plugins.Enable = []string{"invites"}
	if err := g.SetFeatures(context.Background(), f); err != nil {
		t.Fatalf("set features: %v", err)
	}
	if !switchedOn(g, "invites") {
		t.Fatal("invites off after switching them on")
	}
	if raw, _, _ := db.Features(context.Background()); !strings.Contains(string(raw), `"invites"`) {
		t.Errorf("not saved: %s", raw)
	}
	// Invalid settings change nothing.
	f.Plugins.Enable = []string{"requests"} // no such feature
	if err := g.SetFeatures(context.Background(), f); err == nil {
		t.Error("invalid features accepted")
	}
	if !switchedOn(g, "invites") {
		t.Error("running settings lost")
	}

	// Reloading the file keeps the database's features, even if the file
	// says otherwise.
	write("[plugins]\nenable = []\n")
	if err := g.reload(env("a")); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !switchedOn(g, "invites") {
		t.Error("the file overrode the panel")
	}

	// What can't change live, or can't load, is refused as a whole.
	refused := map[string]func(){
		"broken file": func() { write("[plugins\n") },
		"new listen address": func() {
			write("")
			os.WriteFile(cfgPath, bytes.Replace(must(os.ReadFile(cfgPath)), []byte(`":8080"`), []byte(`":9090"`), 1), 0o600)
		},
	}
	for name, change := range refused {
		change()
		if err := g.reload(env("a")); err == nil {
			t.Errorf("%s: reloaded", name)
		}
		if !switchedOn(g, "invites") {
			t.Errorf("%s: running configuration lost", name)
		}
	}
	write("")
	if err := g.reload(env("b")); err == nil || !strings.Contains(err.Error(), "OVERFIN_SECRET_KEY") {
		t.Errorf("new secret key: %v", err)
	}
}

// On the first start, the file's feature settings fill the empty database.
func TestFeaturesImportedOnce(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	os.WriteFile(cfgPath, []byte(strings.ReplaceAll(baseConfig, "%DB%", dbPath)+"[plugins]\nenable = [\"library\"]\n"), 0o600)
	cfg, err := config.Load(cfgPath, env("a"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, _ := store.Open(context.Background(), dbPath, box)
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, err := effective(context.Background(), cfg, db, logger)
	if err != nil || !got.Plugins.Enabled("library") {
		t.Fatalf("import: %v %v", got.Plugins, err)
	}
	// Later, the file no longer decides.
	cfg.Plugins.Enable = nil
	if got, _ := effective(context.Background(), cfg, db, logger); !got.Plugins.Enabled("library") {
		t.Error("the file overrode the database")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// Settings saved when bringing accounts over was the core's are carried over
// to the migration module, once.
func TestAdoptionMovesToTheMigrationModule(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	os.WriteFile(cfgPath, []byte(strings.ReplaceAll(baseConfig, "%DB%", dbPath)), 0o600)
	cfg, err := config.Load(cfgPath, env("a"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, _ := store.Open(context.Background(), dbPath, box)
	defer db.Close()
	ctx := context.Background()
	db.SetFeatures(ctx, []byte(`{"limits":{"maxTranscodesTotal":4,"pinAttempts":5,"onLimit":"replace-oldest"},`+
		`"adoption":{"claim":true,"mode":"link"},"plugins":{"enable":["library"]},"accounts":{"onDelete":"delete"}}`))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, err := effective(ctx, cfg, db, logger)
	if err != nil || !got.Plugins.Enabled("migration") || !got.Plugins.Enabled("library") || got.Modules["migration"]["mode"] != "link" {
		t.Fatalf("carried over: %v %v %v", got.Plugins, got.Modules, err)
	}
	if raw, _, _ := db.Features(ctx); bytes.Contains(raw, []byte("adoption")) || !bytes.Contains(raw, []byte(`"mode":"link"`)) {
		t.Errorf("saved: %s", raw)
	}
}

func TestFirstKey(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	setup := func(t *testing.T, dbExists bool) *config.Config {
		dir := t.TempDir()
		config.KeyFile = filepath.Join(dir, "secrets", "overfin.key")
		t.Cleanup(func() { config.KeyFile = "/secrets/overfin.key" })
		os.Mkdir(filepath.Dir(config.KeyFile), 0o700)
		cfg := &config.Config{Storage: config.Storage{Path: filepath.Join(dir, "overfin.db")}}
		if dbExists {
			os.WriteFile(cfg.Storage.Path, []byte("data"), 0o600)
		}
		return cfg
	}

	// A first start: the key is made, and kept for the next starts.
	cfg := setup(t, false)
	if err := firstKey(cfg, false, logger); err != nil || len(cfg.Secrets.Key) != config.MinKeyBytes {
		t.Fatalf("first start: %v", err)
	}
	if _, err := os.Stat(config.KeyFile); err != nil {
		t.Errorf("key file: %v", err)
	}

	// A database without its key: a lost key, never replaced quietly.
	cfg = setup(t, true)
	if err := firstKey(cfg, false, logger); err == nil || !strings.Contains(err.Error(), "-recover-tokens") {
		t.Errorf("existing database: %v", err)
	}
	if _, err := os.Stat(config.KeyFile); err == nil {
		t.Error("a key was made for an existing database")
	}
	// Unless that's what the operator is recovering from.
	if err := firstKey(cfg, true, logger); err != nil || cfg.Secrets.Key == nil {
		t.Errorf("recovering: %v", err)
	}
}

// Jellyfin saved by the setup page isn't in the file: a reload keeps it.
func TestReloadKeepsSavedJellyfin(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	content := strings.ReplaceAll(baseConfig, "%DB%", dbPath)
	content = strings.Replace(content, "[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n", "", 1)
	os.WriteFile(cfgPath, []byte(content), 0o600)
	noAdminKey := func(name string) string {
		if name == "OVERFIN_JELLYFIN_ADMIN_KEY" {
			return ""
		}
		return env("a")(name)
	}
	cfg, err := config.Load(cfgPath, noAdminKey)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = "http://192.0.2.10:8096", "saved-key"
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sh, err := newShared(cfg, db, nil, k, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	g, err := newGateway(cfgPath, cfg, sh)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.reload(noAdminKey); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if g.cfg.Jellyfin.URL != "http://192.0.2.10:8096" || g.cfg.Secrets.JellyfinAdminKey != "saved-key" {
		t.Errorf("after reload: %q", g.cfg.Jellyfin.URL)
	}
}

// The panel changes Jellyfin's address live, for the same server only.
func TestSetJellyfin(t *testing.T) {
	first := fakeJellyfinServer(t, "12.2.0", "server-1").URL
	sameServer := fakeJellyfinServer(t, "12.2.0", "server-1").URL
	otherServer := fakeJellyfinServer(t, "12.2.0", "server-2").URL

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	content := strings.ReplaceAll(baseConfig, "%DB%", dbPath)
	content = strings.Replace(content, "[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n", "", 1)
	os.WriteFile(cfgPath, []byte(content), 0o600)
	noAdminKey := func(name string) string {
		if name == "OVERFIN_JELLYFIN_ADMIN_KEY" {
			return ""
		}
		return env("a")(name)
	}
	cfg, err := config.Load(cfgPath, noAdminKey)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// As the setup page left it.
	db.SetJellyfin(context.Background(), first, "admin", "server-1")
	cfg.Jellyfin.URL, cfg.Secrets.JellyfinAdminKey = first, "admin"
	sh, err := newShared(cfg, db, nil, k, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	sh.jellyfinSaved = true
	g, err := newGateway(cfgPath, cfg, sh)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Another server is refused, and nothing changes.
	if _, err := g.SetJellyfin(ctx, otherServer, ""); err == nil || !strings.Contains(err.Error(), "another Jellyfin server") {
		t.Errorf("another server: %v", err)
	}
	// A key that doesn't work is refused too.
	if _, err := g.SetJellyfin(ctx, sameServer, "nope"); err == nil {
		t.Error("a refused key was saved")
	}
	if url, _ := g.Jellyfin(); url != first {
		t.Errorf("changed after refusals: %s", url)
	}

	// The same server at a new address, the key kept: applied and saved.
	summary, err := g.SetJellyfin(ctx, sameServer+"/", "")
	if err != nil || !strings.Contains(summary, "Jellyfin 12.2.0") {
		t.Fatalf("same server: %q %v", summary, err)
	}
	if url, inFile := g.Jellyfin(); url != sameServer || inFile {
		t.Errorf("in effect: %s, in file %v", url, inFile)
	}
	if n, err := sh.jf.CountLibraries(ctx, jellyfin.Auth{Token: "admin", DeviceID: "t"}); err != nil || n != 2 {
		t.Errorf("the client doesn't reach the new address: %d %v", n, err)
	}
	if url, key, _, _ := db.Jellyfin(ctx); url != sameServer || key != "admin" {
		t.Errorf("saved: %s %s", url, key)
	}

	// Set in the file: the panel can't change it.
	sh.jellyfinSaved = false
	if _, err := g.SetJellyfin(ctx, first, ""); err == nil || !strings.Contains(err.Error(), "configuration file") {
		t.Errorf("in the file: %v", err)
	}
}

// The panel connects and disconnects Seerr; discovery follows at once.
func TestSetSeerr(t *testing.T) {
	seerrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "seerr-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/status":
			io.WriteString(w, `{"version":"3.5.0"}`)
		case "/api/v1/auth/me":
			io.WriteString(w, `{"id":1}`)
		}
	}))
	defer seerrSrv.Close()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "overfin.db")
	cfgPath := filepath.Join(dir, "overfin.toml")
	os.WriteFile(cfgPath, []byte(strings.ReplaceAll(baseConfig, "%DB%", dbPath)), 0o600)
	cfg, err := config.Load(cfgPath, env("a"))
	if err != nil {
		t.Fatal(err)
	}
	k, _ := keys.Derive(cfg.Secrets.Key)
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sh, err := newShared(cfg, db, nil, k, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	g, err := newGateway(cfgPath, cfg, sh)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if code := status(g, "/api/v1/discover"); code != 404 {
		t.Fatalf("discover before Seerr: %d", code)
	}
	if _, err := g.SetSeerr(ctx, seerrSrv.URL, "wrong"); err == nil || !strings.Contains(err.Error(), "refuses this API key") {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := g.SetSeerr(ctx, seerrSrv.URL+"/", "seerr-key"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if g.Seerr() != seerrSrv.URL || status(g, "/api/v1/discover") == 404 {
		t.Errorf("after connecting: %q, discover %d", g.Seerr(), status(g, "/api/v1/discover"))
	}
	if url, key, ok, _ := db.Seerr(ctx); !ok || url != seerrSrv.URL || key != "seerr-key" {
		t.Errorf("saved: %q %q %v", url, key, ok)
	}
	// An empty key keeps the current one.
	if _, err := g.SetSeerr(ctx, seerrSrv.URL, ""); err != nil {
		t.Errorf("same key: %v", err)
	}
	// Disconnected: discovery is gone, and so are the settings.
	if _, err := g.SetSeerr(ctx, "", ""); err != nil || status(g, "/api/v1/discover") != 404 {
		t.Errorf("disconnect: %v", err)
	}
	if _, _, ok, _ := db.Seerr(ctx); ok {
		t.Error("settings kept after disconnecting")
	}
}
