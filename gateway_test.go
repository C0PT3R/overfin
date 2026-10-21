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
	"sync"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/config"
	"github.com/C0PT3R/overfin-gateway/internal/images"
	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/keys"
	"github.com/C0PT3R/overfin-gateway/internal/playback"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
	"github.com/C0PT3R/overfin-gateway/internal/store"
	"github.com/C0PT3R/overfin-gateway/module"
	"github.com/C0PT3R/overfin-gateway/modules/migration"
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
	// The first start imported the file's feature settings (none): no
	// plugins, so the library route doesn't exist.
	if code := status(g, "/api/v1/libraries"); code != 404 {
		t.Fatalf("library before enabling: %d", code)
	}

	// The panel enables a plugin: it applies at once, and is saved.
	f := g.Features()
	f.Plugins.Enable = []string{"library"}
	if err := g.SetFeatures(context.Background(), f); err != nil {
		t.Fatalf("set features: %v", err)
	}
	if code := status(g, "/api/v1/libraries"); code != 401 {
		t.Fatalf("library after enabling: %d", code)
	}
	if raw, _, _ := db.Features(context.Background()); !strings.Contains(string(raw), `"library"`) {
		t.Errorf("not saved: %s", raw)
	}
	// Invalid settings change nothing.
	f.Plugins.Enable = []string{"playback"} // needs library
	if err := g.SetFeatures(context.Background(), f); err == nil {
		t.Error("invalid features accepted")
	}
	if code := status(g, "/api/v1/libraries"); code != 401 {
		t.Errorf("running settings lost: %d", code)
	}

	// Reloading the file keeps the database's features, even if the file
	// says otherwise.
	write("[plugins]\nenable = []\n")
	if err := g.reload(env("a")); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if code := status(g, "/api/v1/libraries"); code != 401 {
		t.Errorf("the file overrode the panel: %d", code)
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
		if code := status(g, "/api/v1/libraries"); code != 401 {
			t.Errorf("%s: running configuration lost (%d)", name, code)
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

// registerMigration compiles the migration module in, as the official
// build does, once for all tests.
var registerMigration = sync.OnceValue(func() error {
	modules = []module.Module{migration.Module{}}
	return registerModules()
})

// Settings saved when bringing accounts over was the core's are carried over
// to the migration module, once.
func TestAdoptionMovesToTheMigrationModule(t *testing.T) {
	if err := registerMigration(); err != nil {
		t.Fatal(err)
	}
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
