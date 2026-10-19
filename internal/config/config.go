// Package config loads and validates Overfin's configuration.
//
// Settings come from a TOML file. Secrets never do: they come from
// environment variables, or from files named by NAME_FILE variables (the
// Docker secrets convention). Anything unknown or invalid stops Overfin at
// startup instead of being guessed at.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the whole configuration. Field tags give the TOML key names.
type Config struct {
	Listen   string   `toml:"listen"`
	Jellyfin Jellyfin `toml:"jellyfin"`
	Identity Identity `toml:"identity"`
	Storage  Storage  `toml:"storage"`
	Limits   Limits   `toml:"limits"`
	Adoption Adoption `toml:"adoption"`

	// Secrets, loaded from the environment, never from the file.
	Secrets Secrets `toml:"-"`
}

type Jellyfin struct {
	// URL is where Overfin reaches Jellyfin, e.g. "http://192.0.2.10:8096".
	URL string `toml:"url"`
}

type Identity struct {
	// Mode is "forwardauth" or "anonymous".
	Mode string `toml:"mode"`
	// TrustedProxies are the addresses allowed to send identity headers.
	TrustedProxies []string `toml:"trusted_proxies"`
	// UserHeader carries the stable user ID set by the proxy.
	UserHeader string `toml:"user_header"`
	// GroupsHeader carries the user's groups, separated by "|" or ",".
	GroupsHeader string `toml:"groups_header"`
	// RequiredGroup, when set, is the group a user must be in to be
	// provisioned.
	RequiredGroup string `toml:"required_group"`

	// Parsed form of TrustedProxies, filled in by Load.
	Proxies []netip.Prefix `toml:"-"`
}

// Adoption lets people bring an existing media server account (and its
// history) instead of getting a new one.
type Adoption struct {
	// Claim lets a user adopt an existing Jellyfin account by entering its
	// password once. Off by default.
	Claim bool `toml:"claim"`
}

type Storage struct {
	// Path is the database file.
	Path string `toml:"path"`
}

type Limits struct {
	MaxStreamsPerAccount int `toml:"max_streams_per_account"`
	MaxTranscodesTotal   int `toml:"max_transcodes_total"`
	// MaxBitrateKbps caps every stream; 0 means no cap.
	MaxBitrateKbps int `toml:"max_bitrate_kbps"`
}

type Secrets struct {
	// JellyfinAdminKey is used only for provisioning (SECURITY.md rule 3).
	JellyfinAdminKey string
	// Key encrypts tokens and keys PIN hashes, cursors and log IDs.
	Key []byte
}

// MinKeyBytes is the minimum length of the secret key.
const MinKeyBytes = 32

// defaults returns the settings used when the file leaves them out.
func defaults() Config {
	return Config{
		Listen:  ":8080",
		Storage: Storage{Path: "/data/overfin.db"},
		// No default identity headers: they depend on the proxy, and naming
		// them explicitly avoids trusting the wrong one by accident.
		Identity: Identity{Mode: "forwardauth"},
		Limits: Limits{
			MaxStreamsPerAccount: 2,
			MaxTranscodesTotal:   4,
		},
	}
}

// Load reads the file at path, applies defaults, loads secrets from the
// environment and validates everything. getenv is usually os.Getenv; tests
// pass their own.
func Load(path string, getenv func(string) string) (*Config, error) {
	cfg := defaults()

	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	// A misspelled key would otherwise be silently ignored.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
	}

	if err := loadSecrets(&cfg.Secrets, getenv); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadSecrets(s *Secrets, getenv func(string) string) error {
	adminKey, err := secret("OVERFIN_JELLYFIN_ADMIN_KEY", getenv)
	if err != nil {
		return err
	}
	s.JellyfinAdminKey = adminKey

	encoded, err := secret("OVERFIN_SECRET_KEY", getenv)
	if err != nil {
		return err
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return errors.New("config: OVERFIN_SECRET_KEY must be base64")
	}
	if len(key) < MinKeyBytes {
		return fmt.Errorf("config: OVERFIN_SECRET_KEY must decode to at least %d bytes", MinKeyBytes)
	}
	s.Key = key
	return nil
}

// secret reads a required secret from NAME, or from the file named by
// NAME_FILE. Error messages name the variable but never echo its value.
func secret(name string, getenv func(string) string) (string, error) {
	value := getenv(name)
	file := getenv(name + "_FILE")
	switch {
	case value != "" && file != "":
		return "", fmt.Errorf("config: set %s or %s_FILE, not both", name, name)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("config: reading %s_FILE: %w", name, err)
		}
		value = strings.TrimSpace(string(b))
	}
	if value == "" {
		return "", fmt.Errorf("config: %s is required", name)
	}
	return value, nil
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("config: "+format, args...))
	}

	if c.Listen == "" {
		add("listen must not be empty")
	}

	if err := checkUpstreamURL(c.Jellyfin.URL); err != nil {
		add("jellyfin.url: %v", err)
	}

	switch c.Identity.Mode {
	case "forwardauth":
		if len(c.Identity.TrustedProxies) == 0 {
			add("identity.trusted_proxies is required in forwardauth mode")
		}
		if c.Identity.UserHeader == "" {
			add("identity.user_header is required in forwardauth mode")
		}
		if c.Identity.RequiredGroup != "" && c.Identity.GroupsHeader == "" {
			add("identity.groups_header is required when identity.required_group is set")
		}
	case "anonymous":
	default:
		add("identity.mode must be \"forwardauth\" or \"anonymous\", got %q", c.Identity.Mode)
	}
	for _, p := range c.Identity.TrustedProxies {
		prefix, err := parsePrefix(p)
		if err != nil {
			add("identity.trusted_proxies: %q is not an address or CIDR range", p)
			continue
		}
		c.Identity.Proxies = append(c.Identity.Proxies, prefix)
	}

	if c.Storage.Path == "" {
		add("storage.path must not be empty")
	}

	if c.Limits.MaxStreamsPerAccount < 1 {
		add("limits.max_streams_per_account must be at least 1")
	}
	if c.Limits.MaxTranscodesTotal < 0 {
		add("limits.max_transcodes_total must not be negative")
	}
	if c.Limits.MaxBitrateKbps < 0 {
		add("limits.max_bitrate_kbps must not be negative")
	}

	// errors.Join reports every problem at once, not just the first.
	return errors.Join(errs...)
}

// checkUpstreamURL accepts only a plain http(s) base URL: no credentials,
// query or fragment, so nothing secret or surprising hides in the config.
func checkUpstreamURL(raw string) error {
	if raw == "" {
		return errors.New("is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must start with http:// or https://")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	if u.User != nil {
		return errors.New("must not contain credentials")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must not contain a query or fragment")
	}
	return nil
}

// parsePrefix accepts "192.0.2.1" as well as "192.0.2.0/24".
func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
