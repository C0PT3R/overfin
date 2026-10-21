package overfin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/C0PT3R/overfin-gateway/internal/config"
	"github.com/C0PT3R/overfin-gateway/internal/keys"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

// fakeJellyfin answers what -check asks: its version and server ID, and
// its libraries to the admin key ("admin") only.
func fakeJellyfin(t *testing.T, version string) *httptest.Server {
	return fakeJellyfinServer(t, version, "server-1")
}

func fakeJellyfinServer(t *testing.T, version, id string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprintf(w, `{"Version":%q,"Id":%q}`, version, id)
		case "/Library/MediaFolders":
			switch {
			case strings.Contains(r.Header.Get("Authorization"), `Token="admin"`):
				fmt.Fprint(w, `{"Items":[{"Name":"Movies"},{"Name":"Shows"}]}`)
			case strings.Contains(r.Header.Get("Authorization"), `Token="user"`):
				w.WriteHeader(http.StatusForbidden)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runCheck(t *testing.T, jellyfinURL, dbPath string, getenv func(string) string) (string, error) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "overfin.toml")
	content := strings.ReplaceAll(baseConfig, "%DB%", dbPath)
	content = strings.ReplaceAll(content, "http://192.0.2.10:8096", jellyfinURL)
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := checkSetup(cfgPath, getenv, &out)
	return out.String(), err
}

func TestCheck(t *testing.T) {
	config.KeyFile = filepath.Join(t.TempDir(), "overfin.key")
	t.Cleanup(func() { config.KeyFile = "/secrets/overfin.key" })
	jf := fakeJellyfin(t, "12.2.0").URL
	newDB := filepath.Join(t.TempDir(), "overfin.db")

	out, err := runCheck(t, jf, newDB, env("a"))
	if err != nil || strings.Contains(out, "FAIL") {
		t.Fatalf("a good setup failed (%v):\n%s", err, out)
	}
	for _, want := range []string{"ok    Jellyfin 12.2.0", "2 libraries", "secret key: set", "database: none yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if _, err := os.Stat(newDB); err == nil {
		t.Error("the check created the database")
	}

	// No key yet, and no database: it's a first start.
	noKey := func(name string) string {
		if name == "OVERFIN_SECRET_KEY" {
			return ""
		}
		return env("a")(name)
	}
	if out, err := runCheck(t, jf, newDB, noKey); err != nil || !strings.Contains(out, "generates it on its first start") {
		t.Errorf("first start (%v):\n%s", err, out)
	}

	// A database made with key "a".
	dbPath := filepath.Join(t.TempDir(), "overfin.db")
	k, _ := keys.Derive([]byte(strings.Repeat("a", 32)))
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CheckKey(context.Background(), k.Check); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cases := []struct {
		name, jellyfin, db string
		getenv             func(string) string
		want               string
	}{
		{"its own key", jf, dbPath, env("a"), "ok    secret key: it's the one this database was created with"},
		{"another key", jf, dbPath, env("b"), "FAIL  secret key: it isn't the key this database was created with"},
		{"no key, existing database", jf, dbPath, noKey, "FAIL  secret key: OVERFIN_SECRET_KEY isn't set"},
		{"old Jellyfin", fakeJellyfin(t, "10.11.11").URL, newDB, env("a"), "FAIL  Jellyfin: it runs version 10.11.11"},
		{"Jellyfin unreachable", "http://127.0.0.1:1", newDB, env("a"), "FAIL  Jellyfin: can't reach it"},
		{"refused key", jf, newDB, withAdminKey("nope"), "Jellyfin refuses it"},
		{"not an admin's key", jf, newDB, withAdminKey("user"), "isn't an administrator's"},
	}
	for _, tc := range cases {
		out, err := runCheck(t, tc.jellyfin, tc.db, tc.getenv)
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: missing %q in:\n%s", tc.name, tc.want, out)
		}
		if failed := strings.Contains(out, "FAIL"); failed != (err != nil) {
			t.Errorf("%s: FAIL lines but err = %v", tc.name, err)
		}
	}
	if _, err := os.Stat(config.KeyFile); err == nil {
		t.Error("the check generated a key")
	}
}

func TestCheckBadConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "overfin.toml")
	os.WriteFile(cfgPath, []byte("listen = \"\"\n[jellyfin]\nurl = \"ftp://x\"\n"), 0o600)
	var out bytes.Buffer
	if err := checkSetup(cfgPath, env("a"), &out); err == nil {
		t.Fatal("a bad configuration passed")
	}
	if n := strings.Count(out.String(), "FAIL"); n < 2 {
		t.Errorf("each problem should get its own line:\n%s", out.String())
	}
}

func withAdminKey(key string) func(string) string {
	return func(name string) string {
		if name == "OVERFIN_JELLYFIN_ADMIN_KEY" {
			return key
		}
		return env("a")(name)
	}
}
