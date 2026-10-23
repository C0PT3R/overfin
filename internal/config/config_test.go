package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", MinKeyBytes)))

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

func goodEnv() map[string]string {
	return map[string]string{
		"OVERFIN_SECRET_KEY": testKey,
	}
}

const goodFile = `
listen = ":9000"

[jellyfin]
url = "http://192.0.2.10:8096"

[identity]
mode = "forwardauth"
trusted_proxies = ["198.51.100.7", "198.51.100.0/28"]
user_header = "Remote-User"
`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overfin.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(writeFile(t, goodFile), env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":9000" {
		t.Errorf("Listen = %q, want :9000", cfg.Listen)
	}
	if cfg.Storage.Path != "/data/overfin.db" {
		t.Errorf("default storage path not applied: %q", cfg.Storage.Path)
	}
	if len(cfg.Identity.Proxies) != 2 {
		t.Errorf("parsed %d proxies, want 2", len(cfg.Identity.Proxies))
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "misspelled key",
			file:    goodFile + "\n[limits]\nmax_stream_per_account = 3\n",
			wantErr: "unknown keys: limits.max_stream_per_account",
		},
		{
			name:    "unknown privacy field",
			file:    goodFile + "\n[privacy]\nexpose = [\"user.passwords\"]\n",
			wantErr: "privacy.expose: unknown field",
		},
		{
			name:    "URL with credentials",
			file:    strings.Replace(goodFile, "http://192.0.2.10", "http://user:pass@192.0.2.10", 1),
			wantErr: "must not contain credentials",
		},
		{
			name:    "URL with another scheme",
			file:    strings.Replace(goodFile, "http://192.0.2.10:8096", "file:///etc/passwd", 1),
			wantErr: "must start with http",
		},
		{
			name:    "bad proxy",
			file:    strings.Replace(goodFile, `"198.51.100.7"`, `"not-an-ip"`, 1),
			wantErr: "is not an address or CIDR range",
		},
		{
			name:    "unknown mode",
			file:    strings.Replace(goodFile, `"forwardauth"`, `"magic"`, 1),
			wantErr: "identity.mode",
		},
		{
			name: "short secret key",
			file: goodFile,
			env: map[string]string{
				"OVERFIN_SECRET_KEY": base64.StdEncoding.EncodeToString([]byte("short")),
			},
			wantErr: "at least 32 bytes",
		},
		{
			// Overfin makes its own key on its Jellyfin (SECURITY.md rule 67).
			name:    "a Jellyfin key given",
			file:    goodFile,
			env:     map[string]string{"OVERFIN_SECRET_KEY": testKey, "OVERFIN_JELLYFIN_ADMIN_KEY": "admin-key"},
			wantErr: "isn't used anymore",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vars := tt.env
			if vars == nil {
				vars = goodEnv()
			}
			_, err := Load(writeFile(t, tt.file), env(vars))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestSecretFromFile(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "secret_key")
	if err := os.WriteFile(keyFile, []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := goodEnv()
	delete(vars, "OVERFIN_SECRET_KEY")
	vars["OVERFIN_SECRET_KEY_FILE"] = keyFile

	cfg, err := Load(writeFile(t, goodFile), env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Secrets.Key) != MinKeyBytes {
		t.Errorf("secret key from the file: %d bytes", len(cfg.Secrets.Key))
	}
}

// Secrets must never end up in error messages, which may be logged.
func TestGeneratedKey(t *testing.T) {
	KeyFile = filepath.Join(t.TempDir(), "overfin.key")
	t.Cleanup(func() { KeyFile = "/secrets/overfin.key" })
	vars := map[string]string{}
	path := writeFile(t, goodFile)

	// No key given and none generated yet: Load leaves it to the caller.
	cfg, err := Load(path, env(vars))
	if err != nil || cfg.Secrets.Key != nil {
		t.Fatalf("before generating: key %v, err %v", cfg.Secrets.Key, err)
	}
	key, err := GenerateKey()
	if err != nil || len(key) != MinKeyBytes {
		t.Fatalf("GenerateKey: %d bytes, %v", len(key), err)
	}
	if info, err := os.Stat(KeyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("key file: %v, %v", info, err)
	}
	// Later starts and reloads read it back.
	if cfg, err := Load(path, env(vars)); err != nil || !bytes.Equal(cfg.Secrets.Key, key) {
		t.Errorf("reading it back: %v", err)
	}
	// A key already there is never replaced.
	if _, err := GenerateKey(); err == nil {
		t.Error("GenerateKey replaced an existing key")
	}
	if again, _ := os.ReadFile(KeyFile); !strings.Contains(string(again), base64.StdEncoding.EncodeToString(key)) {
		t.Error("the key file changed")
	}
	// A key given in the environment wins over the generated one.
	given := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	vars["OVERFIN_SECRET_KEY"] = given
	if cfg, err := Load(path, env(vars)); err != nil || cfg.Secrets.Key[0] != 7 {
		t.Errorf("given key: %v", err)
	}
	// An empty key file named on purpose is a mistake, not "no key".
	delete(vars, "OVERFIN_SECRET_KEY")
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, nil, 0o600)
	vars["OVERFIN_SECRET_KEY_FILE"] = empty
	if _, err := Load(path, env(vars)); err == nil || !strings.Contains(err.Error(), "empty file") {
		t.Errorf("empty key file: %v", err)
	}
}

func TestErrorsNeverEchoSecrets(t *testing.T) {
	vars := goodEnv()
	vars["OVERFIN_SECRET_KEY"] = "not base64 but very secret!"
	_, err := Load(writeFile(t, goodFile), env(vars))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "very secret") {
		t.Errorf("error leaks the secret: %v", err)
	}
}

func TestPlugins(t *testing.T) {
	cfg, err := Load(writeFile(t, goodFile), env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins.Enable) != 0 {
		t.Errorf("plugins on by default: %v", cfg.Plugins.Enable)
	}
	// Library, playback and the watchlist are always on; older settings
	// that list them still load.
	for _, name := range AlwaysOn {
		if !cfg.Plugins.Enabled(name) {
			t.Errorf("%s isn't on", name)
		}
	}
	for file, ok := range map[string]bool{
		`enable = ["library", "playback"]`: true,
		`enable = ["playback"]`:            true,
		`enable = ["requests"]`:            false,
	} {
		_, err := Load(writeFile(t, goodFile+"\n[plugins]\n"+file+"\n"), env(goodEnv()))
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", file, err)
		}
	}
}

func TestOIDC(t *testing.T) {
	section := `
[oidc]
issuer = "https://auth.example.org/application/o/site/"
client_id = "overfin"
redirect_url = "https://example.org/api/v1/auth/callback"
dial = "198.51.100.7:443"
`
	oidcMode := strings.Replace(goodFile, `user_header = "Remote-User"`, "", 1)
	oidcMode = strings.Replace(oidcMode, `mode = "forwardauth"`, `mode = "oidc"`, 1)

	vars := goodEnv()
	if _, err := Load(writeFile(t, goodFile+section), env(vars)); err == nil {
		t.Error("[oidc] without its client secret was accepted")
	}
	vars["OVERFIN_OIDC_CLIENT_SECRET"] = "secret"
	cfg, err := Load(writeFile(t, goodFile+section), env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OIDC.Enabled() || cfg.Secrets.OIDCClientSecret != "secret" || cfg.OIDC.FreshForMinutes != 5 || cfg.OIDC.RecheckMinutes != 10 {
		t.Errorf("oidc = %+v", cfg.OIDC)
	}
	if _, err := Load(writeFile(t, oidcMode+section), env(vars)); err != nil {
		t.Errorf("oidc mode: %v", err)
	}
	for _, bad := range []string{
		goodFile + strings.Replace(section, "https://auth", "http://auth", 1),
		goodFile + strings.Replace(section, "/api/v1/auth/callback", "/elsewhere", 1),
		goodFile + strings.Replace(section, `client_id = "overfin"`, `client_id = ""`, 1),
		goodFile + strings.Replace(section, `dial = "198.51.100.7:443"`, `dial = "nowhere"`, 1),
		oidcMode, // oidc mode without [oidc]
	} {
		if _, err := Load(writeFile(t, bad), env(vars)); err == nil {
			t.Errorf("accepted:%s", bad)
		}
	}
	withHeader := strings.Replace(goodFile, `mode = "forwardauth"`, `mode = "oidc"`, 1) + section
	if _, err := Load(writeFile(t, withHeader), env(vars)); err == nil {
		t.Error("oidc mode accepted an identity header")
	}
}

func TestAdmin(t *testing.T) {
	vars := goodEnv()
	vars["OVERFIN_OIDC_CLIENT_SECRET"] = "s"
	oidc := `
[oidc]
issuer = "https://auth.example.org/application/o/site/"
client_id = "overfin"
redirect_url = "https://example.org/api/v1/auth/callback"
`
	ok := `
[admin]
listen = ":8081"
group = "overfin-admins"
redirect_url = "http://localhost:8081/admin/auth/callback"
`
	if _, err := Load(writeFile(t, goodFile+oidc+ok), env(vars)); err != nil {
		t.Fatalf("valid admin: %v", err)
	}
	for name, bad := range map[string]string{
		"no oidc":               goodFile + ok,
		"no group":              goodFile + oidc + strings.Replace(ok, `group = "overfin-admins"`, "", 1),
		"public address":        goodFile + oidc + strings.Replace(ok, `":8081"`, `"203.0.113.5:8081"`, 1),
		"http not on localhost": goodFile + oidc + strings.Replace(ok, "localhost:8081", "admin.example.org", 1),
		"wrong callback":        goodFile + oidc + strings.Replace(ok, "/admin/auth/callback", "/callback", 1),
		"cert without key":      goodFile + oidc + ok + "tls_cert = \"/a.crt\"\n",
	} {
		if _, err := Load(writeFile(t, bad), env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A private address and https are fine.
	lan := strings.Replace(strings.Replace(ok, `":8081"`, `"192.168.1.5:8081"`, 1),
		"http://localhost:8081", "https://admin.example.org", 1) + "tls_cert = \"/a.crt\"\ntls_key = \"/a.key\"\n"
	if _, err := Load(writeFile(t, goodFile+oidc+lan), env(vars)); err != nil {
		t.Errorf("LAN admin: %v", err)
	}
}

// connector.first_admin: an email Pocket ID takes as a username, with
// Pocket ID only.
func TestFirstAdmin(t *testing.T) {
	vars := goodEnv()
	vars["OVERFIN_OIDC_CLIENT_SECRET"] = "s"
	vars["OVERFIN_CONNECTOR_TOKEN"] = "t"
	base := goodFile + `
[oidc]
issuer = "https://auth.example.org"
client_id = "overfin"
redirect_url = "https://example.org/api/v1/auth/callback"

[admin]
listen = ":8081"
group = "overfin-admins"
redirect_url = "http://localhost:8081/admin/auth/callback"

[connector]
url = "https://auth.example.org"
`
	if _, err := Load(writeFile(t, base+"kind = \"pocketid\"\nfirst_admin = \"me.too@example.org\"\n"), env(vars)); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, bad := range map[string]string{
		"authentik":    "kind = \"authentik\"\nfirst_admin = \"me@example.org\"\n",
		"not an email": "kind = \"pocketid\"\nfirst_admin = \"me\"\n",
		"a plus":       "kind = \"pocketid\"\nfirst_admin = \"me+x@example.org\"\n",
		"too long":     "kind = \"pocketid\"\nfirst_admin = \"" + strings.Repeat("a", 40) + "@example.org\"\n",
	} {
		if _, err := Load(writeFile(t, base+bad), env(vars)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestModuleSettings(t *testing.T) {
	RegisterPlugin("settings-test", nil, "", []Setting{
		{Key: "mode", Choices: []Choice{{Value: "a"}, {Value: "b"}}},
		{Key: "loud"},
	})
	t.Cleanup(func() {
		delete(KnownPlugins, "settings-test")
		delete(PluginDescriptions, "settings-test")
		delete(PluginSettings, "settings-test")
	})
	cfg, err := Load(writeFile(t, goodFile), env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Modules["settings-test"]; m["mode"] != "a" || m["loud"] != "off" {
		t.Errorf("defaults: %v", m)
	}
	for file, ok := range map[string]bool{
		`mode = "b"`:    true,
		`loud = "on"`:   true,
		`mode = "c"`:    false,
		`loud = "yes"`:  false,
		`volume = "on"`: false,
	} {
		_, err := Load(writeFile(t, goodFile+"\n[modules.settings-test]\n"+file+"\n"), env(goodEnv()))
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", file, err)
		}
	}
	// A module this build doesn't have keeps its settings, untouched.
	cfg, err = Load(writeFile(t, goodFile+"\n[modules.elsewhere]\nx = \"y\"\n"), env(goodEnv()))
	if err != nil || cfg.Modules["elsewhere"]["x"] != "y" {
		t.Errorf("another build's module: %v %v", cfg.Modules, err)
	}
}

// The former [adoption] section switches the migration module on, in the
// same mode.
func TestAdoptionCarriedOver(t *testing.T) {
	var f Features
	f.ImportAdoption(Adoption{Claim: false, Mode: "link"})
	if f.Plugins.Enabled("migration") {
		t.Error("switched on without claiming")
	}
	f.ImportAdoption(Adoption{Claim: true, Mode: "link"})
	f.ImportAdoption(Adoption{Claim: true, Mode: "link"})
	if len(f.Plugins.Enable) != 1 || !f.Plugins.Enabled("migration") || f.Modules["migration"]["mode"] != "link" {
		t.Errorf("carried over: %v %v", f.Plugins, f.Modules)
	}
	var g Features
	g.ImportAdoption(Adoption{Claim: true})
	if g.Modules["migration"]["mode"] != "move" {
		t.Errorf("default mode: %v", g.Modules)
	}
}

func TestSite(t *testing.T) {
	cfg, err := Load(writeFile(t, goodFile), env(goodEnv()))
	if err != nil || cfg.Site.Serve != "builtin" || cfg.Site.CSP != DefaultCSP {
		t.Fatalf("defaults: %+v %v", cfg.Site, err)
	}
	dir := t.TempDir()
	for file, ok := range map[string]bool{
		`serve = "none"`:                 true,
		`serve = "` + dir + `"`:          true,
		`serve = "site"`:                 false,
		`serve = "/does/not/exist"`:      false,
		`csp = "default-src 'self'"`:     true,
		`csp = ""`:                       false,
		"csp = \"default-src\\n'self'\"": false,
		`spa_fallback = true`:            true,
	} {
		_, err := Load(writeFile(t, goodFile+"\n[site]\n"+file+"\n"), env(goodEnv()))
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", file, err)
		}
	}
}

// Jellyfin's address is optional: without it, the setup page asks.
func TestJellyfinAddressOptional(t *testing.T) {
	noJellyfin := strings.Replace(goodFile, "[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n", "", 1)
	for name, file := range map[string]string{"with": goodFile, "without": noJellyfin} {
		if _, err := Load(writeFile(t, file), env(goodEnv())); err != nil {
			t.Errorf("%s an address: %v", name, err)
		}
	}
}

func TestManaged(t *testing.T) {
	// Nothing about identity: managed sign-in, people sign in with
	// passwords until there's a mail server.
	cfg, err := Load(writeFile(t, "[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n"), env(goodEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Identity.Mode != "managed" || cfg.Mail.Enabled() {
		t.Errorf("defaults: mode %q, mail %+v", cfg.Identity.Mode, cfg.Mail)
	}

	// A mail server, with its defaults and password.
	vars := goodEnv()
	vars["OVERFIN_MAIL_PASSWORD"] = "smtp-secret"
	vars["OVERFIN_OIDC_CLIENT_SECRET"] = "oidc-secret"
	cfg, err = Load(writeFile(t, `
[jellyfin]
url = "http://192.0.2.10:8096"
[site]
url = "https://media.example.org"
[mail]
host = "smtp.example.org"
from = "Media <media@example.org>"
username = "media@example.org"
`), env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Mail; m.Port != 587 || m.TLS != "starttls" || cfg.Secrets.MailPassword != "smtp-secret" {
		t.Errorf("mail: %+v", m)
	}

	for file, want := range map[string]string{
		// Written when forwardauth was the default.
		"[identity]\ntrusted_proxies = [\"198.51.100.7\"]\nuser_header = \"Remote-User\"\n":                 `add mode = "forwardauth"`,
		"[identity]\nrequired_group = \"media\"\n":                                                          "required_group",
		"[mail]\nhost = \"smtp.example.org\"\nfrom = \"not an address\"\n":                                  "mail.from",
		"[mail]\nhost = \"smtp.example.org\"\nfrom = \"a@example.org\"\ntls = \"none\"\nusername = \"a\"\n": "needs TLS",
		"[mail]\nfrom = \"a@example.org\"\n":                                                                "mail.host is required",
		"[site]\nurl = \"https://media.example.org/movies\"\n":                                              "site.url",
		"[oidc]\nissuer = \"https://auth.example.org\"\n":                                                   "[oidc] is for",
	} {
		_, err := Load(writeFile(t, file), env(vars))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", file, err, want)
		}
	}
}

func TestNoFileAndHTTPS(t *testing.T) {
	// No file at all: the defaults, and the domain from the environment.
	vars := goodEnv()
	vars["OVERFIN_DOMAIN"] = "Media.Example.org."
	cfg, err := Load("", env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Identity.Mode != "managed" || cfg.HTTPS.Domain != "media.example.org" || cfg.HTTPS.Listen != ":8443" ||
		cfg.Site.URL != "https://media.example.org" {
		t.Errorf("no file: %+v %+v %q", cfg.Identity, cfg.HTTPS, cfg.Site.URL)
	}
	delete(vars, "OVERFIN_DOMAIN")
	for file, want := range map[string]string{
		"[https]\ndomain = \"192.0.2.10\"\n":                                                     "https.domain",
		"[https]\ndomain = \"localhost\"\n":                                                      "https.domain",
		"listen = \":8443\"\n[https]\ndomain = \"media.example.org\"\n":                          "must differ",
		"[https]\ndomain = \"media.example.org\"\nemail = \"x\"\n":                               "https.email",
		"[site]\nurl = \"https://other.example.org\"\n[https]\ndomain = \"media.example.org\"\n": "site.url must be https://media.example.org",
	} {
		_, err := Load(writeFile(t, file), env(vars))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error %v, want %q", file, err, want)
		}
	}
}

func TestManagedPanelListener(t *testing.T) {
	vars := goodEnv()
	if _, err := Load(writeFile(t, "[jellyfin]\nurl = \"http://192.0.2.10:8096\"\n[admin]\nlisten = \":8081\"\n"), env(vars)); err != nil {
		t.Errorf("managed, panel on its own listener: %v", err)
	}
	for file, want := range map[string]string{
		"[admin]\nlisten = \":8081\"\nprivate = true\n":     "don't go together",
		"[admin]\nlisten = \"198.51.100.7:8081\"\n":         "private address",
		"[admin]\nlisten = \":8081\"\ngroup = \"admins\"\n": "takes only listen",
	} {
		_, err := Load(writeFile(t, file), env(vars))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", file, err, want)
		}
	}
}
