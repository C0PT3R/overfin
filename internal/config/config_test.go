package config

import (
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
		"OVERFIN_JELLYFIN_ADMIN_KEY": "admin-key",
		"OVERFIN_SECRET_KEY":         testKey,
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
	if cfg.Secrets.JellyfinAdminKey != "admin-key" {
		t.Error("admin key not loaded")
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
			name: "missing secret key",
			file: goodFile,
			env: map[string]string{
				"OVERFIN_JELLYFIN_ADMIN_KEY": "admin-key",
			},
			wantErr: "OVERFIN_SECRET_KEY is required",
		},
		{
			name: "short secret key",
			file: goodFile,
			env: map[string]string{
				"OVERFIN_JELLYFIN_ADMIN_KEY": "admin-key",
				"OVERFIN_SECRET_KEY":         base64.StdEncoding.EncodeToString([]byte("short")),
			},
			wantErr: "at least 32 bytes",
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
	keyFile := filepath.Join(t.TempDir(), "admin_key")
	if err := os.WriteFile(keyFile, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vars := goodEnv()
	delete(vars, "OVERFIN_JELLYFIN_ADMIN_KEY")
	vars["OVERFIN_JELLYFIN_ADMIN_KEY_FILE"] = keyFile

	cfg, err := Load(writeFile(t, goodFile), env(vars))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secrets.JellyfinAdminKey != "from-file" {
		t.Errorf("admin key = %q, want %q", cfg.Secrets.JellyfinAdminKey, "from-file")
	}
}

// Secrets must never end up in error messages, which may be logged.
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
	for file, ok := range map[string]bool{
		`enable = ["library", "playback"]`: true,
		`enable = ["library"]`:             true,
		`enable = ["playback"]`:            false,
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
