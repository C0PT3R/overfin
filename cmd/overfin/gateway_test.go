package main

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

	"github.com/C0PT3R/overfin/internal/config"
	"github.com/C0PT3R/overfin/internal/images"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/keys"
	"github.com/C0PT3R/overfin/internal/playback"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/store"
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
	// No plugins yet: the library route doesn't exist.
	if code := status(g, "/api/v1/libraries"); code != 404 {
		t.Fatalf("library before enabling: %d", code)
	}

	// Enabling a plugin takes effect at once: the route exists (and asks
	// for a login).
	write("[plugins]\nenable = [\"library\"]\n")
	if err := g.reload(env("a")); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if code := status(g, "/api/v1/libraries"); code != 401 {
		t.Fatalf("library after enabling: %d", code)
	}

	// What can't change live, or can't load, is refused as a whole: the
	// running configuration stays.
	refused := map[string]func(){
		"broken file": func() { write("[plugins\n") },
		"new listen address": func() {
			write("[plugins]\nenable = [\"library\"]\n")
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
	write("[plugins]\nenable = [\"library\"]\n")
	if err := g.reload(env("b")); err == nil || !strings.Contains(err.Error(), "OVERFIN_SECRET_KEY") {
		t.Errorf("new secret key: %v", err)
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
