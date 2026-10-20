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
	"maps"
	"net"
	"net/netip"
	"net/url"
	"os"
	"slices"
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
	Privacy  Privacy  `toml:"privacy"`
	Plugins  Plugins  `toml:"plugins"`
	Accounts Accounts `toml:"accounts"`
	OIDC     OIDC     `toml:"oidc"`
	Admin    Admin    `toml:"admin"`

	// FeaturesInFile reports whether the file held feature settings
	// (limits, adoption, privacy, plugins, accounts). Those live in the
	// database now; the file's are only read to fill an empty one.
	FeaturesInFile bool `toml:"-"`

	// Secrets, loaded from the environment, never from the file.
	Secrets Secrets `toml:"-"`
}

type Jellyfin struct {
	// URL is where Overfin reaches Jellyfin, e.g. "http://192.0.2.10:8096".
	URL string `toml:"url"`
}

type Identity struct {
	// Mode is "oidc" (Overfin logs users in with the [oidc] provider),
	// "forwardauth" (a reverse proxy does, and sends identity headers) or
	// "anonymous".
	Mode string `toml:"mode"`
	// TrustedProxies are the reverse proxies in front of Overfin: they may
	// send identity headers in forwardauth mode, and the client address in
	// every mode.
	TrustedProxies []string `toml:"trusted_proxies"`
	// UserHeader carries the stable user ID set by the proxy.
	UserHeader string `toml:"user_header"`
	// GroupsHeader carries the user's groups, separated by GroupsSeparator.
	GroupsHeader string `toml:"groups_header"`
	// GroupsSeparator separates groups: "|" (Authentik, the default) or ","
	// (Authelia).
	GroupsSeparator string `toml:"groups_separator"`
	// RequiredGroup, when set, is the group a user must be in to be
	// provisioned.
	RequiredGroup string `toml:"required_group"`
	// LogoutURL is where users go to also leave the identity provider,
	// for example "/outpost.goauthentik.io/sign_out" with Authentik.
	LogoutURL string `toml:"logout_url"`

	// Parsed form of TrustedProxies, filled in by Load.
	Proxies []netip.Prefix `toml:"-"`
}

// Adoption lets people bring an existing media server account (and its
// history) instead of getting a new one.
type Adoption struct {
	// Claim lets a user adopt an existing Jellyfin account by entering its
	// password once. Off by default.
	Claim bool `toml:"claim" json:"claim"`
	// Mode is "move" (the default: claiming is one-way, the Jellyfin account
	// gets an opaque name, the lock-down and a new password nobody knows) or
	// "link" (it keeps its name and password, for a transition; the
	// operator moves it later with overfin -move-linked).
	Mode string `toml:"mode" json:"mode"`
}

// Privacy lists the privacy-sensitive fields the operator chooses to
// expose (SECURITY.md rule 55). Everything not listed stays hidden.
type Privacy struct {
	Expose []string `toml:"expose" json:"expose"`
}

// ExposableFields are the privacy-sensitive fields an operator may turn on.
var ExposableFields = []string{"version.names", "photo.takenAt", "photo.location", "photo.camera", "item.tags"}

// Exposes reports whether the operator turned on a privacy-sensitive field.
func (p Privacy) Exposes(field string) bool {
	return slices.Contains(p.Expose, field)
}

// OIDC is Overfin's client registration at the identity provider. It logs
// users in (identity mode "oidc") and confirms fresh logins before
// destructive actions (step-up, in any mode). Off unless Issuer is set.
type OIDC struct {
	// Issuer is the provider's issuer URL, exactly as it states it.
	Issuer   string `toml:"issuer"`
	ClientID string `toml:"client_id"`
	// RedirectURL is this site's callback, registered at the provider:
	// "https://<site>/api/v1/auth/callback".
	RedirectURL string `toml:"redirect_url"`
	// Dial, when set, is the "host:port" Overfin connects to for the
	// provider, instead of what DNS says (TLS is still checked against the
	// issuer's name).
	Dial string `toml:"dial"`
	// FreshForMinutes is how long a confirmed login stays fresh for
	// step-up.
	FreshForMinutes int `toml:"fresh_for_minutes"`
	// RecheckMinutes is how often a login is confirmed again with the
	// provider: a user disabled there is out within that time.
	RecheckMinutes int `toml:"recheck_minutes"`
	// GroupsClaim is the ID token claim listing groups, used with
	// identity.required_group.
	GroupsClaim string `toml:"groups_claim"`
}

// Enabled reports whether an OpenID Connect provider is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

type Accounts struct {
	// OnDelete is what happens to the Jellyfin users of a deleted account:
	// "delete" (the default: with their watch history) or "disable" (kept,
	// with their history, but unusable).
	OnDelete string `toml:"on_delete" json:"onDelete"`
}

type Plugins struct {
	// Enable lists the plugins this instance offers. None are on by
	// default: without any, Overfin only manages accounts and profiles.
	Enable []string `toml:"enable" json:"enable"`
}

// KnownPlugins are the plugins an operator may enable, and what each one
// needs enabled with it.
var KnownPlugins = map[string][]string{
	"library":  nil,
	"playback": {"library"},
}

// Enabled reports whether the operator enabled a plugin.
func (p Plugins) Enabled(name string) bool {
	return slices.Contains(p.Enable, name)
}

// Features are the settings the control panel switches on and off. They
// live in the database and apply at once.
type Features struct {
	Limits   Limits   `json:"limits"`
	Adoption Adoption `json:"adoption"`
	Privacy  Privacy  `json:"privacy"`
	Plugins  Plugins  `json:"plugins"`
	Accounts Accounts `json:"accounts"`
}

// Features returns the configuration's feature settings.
func (c *Config) Features() Features {
	return Features{Limits: c.Limits, Adoption: c.Adoption, Privacy: c.Privacy, Plugins: c.Plugins, Accounts: c.Accounts}
}

// DefaultFeatures are the feature settings of a new instance: everything
// optional off.
func DefaultFeatures() Features {
	d := defaults()
	return d.Features()
}

// WithFeatures returns a copy of the configuration with other feature
// settings, validated together with the rest.
func (c *Config) WithFeatures(f Features) (*Config, error) {
	n := *c
	n.Limits, n.Adoption, n.Privacy, n.Plugins, n.Accounts = f.Limits, f.Adoption, f.Privacy, f.Plugins, f.Accounts
	n.Identity.Proxies = nil // filled again by validate
	if err := n.validate(); err != nil {
		return nil, err
	}
	return &n, nil
}

// Admin is Overfin's control panel: its own listener, never on the internet
// (SECURITY.md rule 58). Off unless Listen is set.
type Admin struct {
	// Listen is the panel's address, e.g. ":8081" in a container whose port
	// is published on a private address only.
	Listen string `toml:"listen"`
	// Group is the identity provider group whose members are admins.
	Group string `toml:"group"`
	// RedirectURL is the panel's login callback, registered at the
	// provider: "http://localhost:8081/admin/auth/callback" through an SSH
	// tunnel, or an https:// address on the LAN or VPN with TLS below.
	RedirectURL string `toml:"redirect_url"`
	// TLSCert and TLSKey, when set, serve the panel over HTTPS.
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
}

// Enabled reports whether the control panel is on.
func (a Admin) Enabled() bool { return a.Listen != "" }

type Storage struct {
	// Path is the database file.
	Path string `toml:"path"`
}

type Limits struct {
	MaxProfilesPerAccount int `toml:"max_profiles_per_account" json:"maxProfilesPerAccount"`
	MaxStreamsPerAccount  int `toml:"max_streams_per_account" json:"maxStreamsPerAccount"`
	MaxTranscodesTotal    int `toml:"max_transcodes_total" json:"maxTranscodesTotal"`
	// MaxBitrateKbps caps every stream; 0 means no cap.
	MaxBitrateKbps int `toml:"max_bitrate_kbps" json:"maxBitrateKbps"`
	// PINAttempts is how many wrong PINs in a row lock a profile.
	PINAttempts int `toml:"pin_attempts" json:"pinAttempts"`
	// OnLimit is what happens when an account starts one stream too many:
	// "replace-oldest" (the default) or "refuse".
	OnLimit string `toml:"on_limit" json:"onLimit"`
}

type Secrets struct {
	// JellyfinAdminKey is used only for provisioning (SECURITY.md rule 3).
	JellyfinAdminKey string
	// Key encrypts tokens and keys PIN hashes, cursors and log IDs.
	Key []byte
	// OIDCClientSecret is the client secret at the identity provider,
	// required only with [oidc].
	OIDCClientSecret string
}

// PrivateAddr reports whether ip is private: loopback, link-local, the
// private IPv4 ranges, the shared range VPNs use (100.64.0.0/10), and
// unique-local IPv6.
func PrivateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// MinKeyBytes is the minimum length of the secret key.
const MinKeyBytes = 32

// defaults returns the settings used when the file leaves them out.
func defaults() Config {
	return Config{
		Listen:  ":8080",
		Storage: Storage{Path: "/data/overfin.db"},
		// No default identity headers: they depend on the proxy, and naming
		// them explicitly avoids trusting the wrong one by accident.
		Identity: Identity{Mode: "forwardauth", GroupsSeparator: "|"},
		Limits: Limits{
			MaxProfilesPerAccount: 5,
			MaxStreamsPerAccount:  2,
			MaxTranscodesTotal:    4,
			PINAttempts:           5,
			OnLimit:               "replace-oldest",
		},
		OIDC:     OIDC{FreshForMinutes: 5, RecheckMinutes: 10, GroupsClaim: "groups"},
		Adoption: Adoption{Mode: "move"},
		Accounts: Accounts{OnDelete: "delete"},
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

	for _, section := range []string{"limits", "adoption", "privacy", "plugins", "accounts"} {
		if meta.IsDefined(section) {
			cfg.FeaturesInFile = true
		}
	}

	if err := loadSecrets(&cfg.Secrets, getenv); err != nil {
		return nil, err
	}
	if cfg.OIDC.Enabled() {
		s, err := secret("OVERFIN_OIDC_CLIENT_SECRET", getenv)
		if err != nil {
			return nil, err
		}
		cfg.Secrets.OIDCClientSecret = s
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
	case "oidc":
		if !c.OIDC.Enabled() {
			add("identity.mode = \"oidc\" needs the [oidc] section")
		}
		if c.Identity.UserHeader != "" || c.Identity.GroupsHeader != "" {
			add("identity.user_header and groups_header are for forwardauth; oidc mode reads no identity headers")
		}
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
		add("identity.mode must be \"oidc\", \"forwardauth\" or \"anonymous\", got %q", c.Identity.Mode)
	}
	if c.Identity.GroupsSeparator != "|" && c.Identity.GroupsSeparator != "," {
		add("identity.groups_separator must be \"|\" or \",\"")
	}
	for _, p := range c.Identity.TrustedProxies {
		prefix, err := parsePrefix(p)
		if err != nil {
			add("identity.trusted_proxies: %q is not an address or CIDR range", p)
			continue
		}
		c.Identity.Proxies = append(c.Identity.Proxies, prefix)
	}

	for _, f := range c.Privacy.Expose {
		if !slices.Contains(ExposableFields, f) {
			add("privacy.expose: unknown field %q (known: %s)", f, strings.Join(ExposableFields, ", "))
		}
	}

	known := slices.Sorted(maps.Keys(KnownPlugins))
	for _, name := range c.Plugins.Enable {
		needs, ok := KnownPlugins[name]
		if !ok {
			add("plugins.enable: unknown plugin %q (known: %s)", name, strings.Join(known, ", "))
		}
		for _, n := range needs {
			if !c.Plugins.Enabled(n) {
				add("plugins.enable: %q needs %q enabled too", name, n)
			}
		}
	}

	if o := c.OIDC; o.Enabled() {
		if u, err := url.Parse(o.Issuer); err != nil || u.Scheme != "https" || u.Host == "" {
			add("oidc.issuer must be an https URL")
		}
		if o.ClientID == "" {
			add("oidc.client_id is required")
		}
		if u, err := url.Parse(o.RedirectURL); err != nil || u.Scheme != "https" || u.Host == "" ||
			u.Path != "/api/v1/auth/callback" || u.RawQuery != "" {
			add("oidc.redirect_url must be https://<your site>/api/v1/auth/callback")
		}
		if o.Dial != "" {
			if _, _, err := net.SplitHostPort(o.Dial); err != nil {
				add("oidc.dial must be host:port")
			}
		}
		if o.FreshForMinutes < 1 || o.FreshForMinutes > 60 {
			add("oidc.fresh_for_minutes must be between 1 and 60")
		}
		if o.RecheckMinutes < 1 || o.RecheckMinutes > 60 {
			add("oidc.recheck_minutes must be between 1 and 60")
		}
		if c.Identity.RequiredGroup != "" && o.GroupsClaim == "" && c.Identity.Mode == "oidc" {
			add("oidc.groups_claim is required when identity.required_group is set")
		}
		if c.Identity.Mode == "anonymous" {
			add("[oidc] can't be used with identity.mode = \"anonymous\"")
		}
	}

	if c.Accounts.OnDelete != "delete" && c.Accounts.OnDelete != "disable" {
		add("accounts.on_delete must be \"delete\" or \"disable\", got %q", c.Accounts.OnDelete)
	}
	if c.Adoption.Mode != "move" && c.Adoption.Mode != "link" {
		add("adoption.mode must be \"move\" or \"link\", got %q", c.Adoption.Mode)
	}

	if a := c.Admin; a.Enabled() {
		if !c.OIDC.Enabled() {
			add("the admin panel needs the [oidc] section, for its sign-in")
		}
		if a.Group == "" {
			add("admin.group is required: only its members may use the panel")
		}
		if host, _, err := net.SplitHostPort(a.Listen); err != nil {
			add("admin.listen must be host:port, e.g. \":8081\"")
		} else if ip, err := netip.ParseAddr(host); host != "" && (err != nil || !PrivateAddr(ip)) {
			add("admin.listen must be a private address, or none (\":8081\"): the panel is never on the internet")
		}
		u, err := url.Parse(a.RedirectURL)
		switch {
		case err != nil || u.Path != "/admin/auth/callback" || u.RawQuery != "":
			add("admin.redirect_url must be <panel address>/admin/auth/callback")
		case u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1":
			add("admin.redirect_url may only use http:// on localhost (through an SSH tunnel); use https:// otherwise")
		case u.Scheme != "http" && u.Scheme != "https":
			add("admin.redirect_url must be an http:// (localhost) or https:// address")
		}
		if (a.TLSCert == "") != (a.TLSKey == "") {
			add("admin.tls_cert and admin.tls_key go together")
		}
	}

	if c.Storage.Path == "" {
		add("storage.path must not be empty")
	}

	if c.Limits.MaxProfilesPerAccount < 1 {
		add("limits.max_profiles_per_account must be at least 1")
	}
	if u := c.Identity.LogoutURL; u != "" && !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "https://") {
		add("identity.logout_url must be a path or an https:// URL")
	}
	if c.Limits.PINAttempts < 3 || c.Limits.PINAttempts > 10 {
		add("limits.pin_attempts must be between 3 and 10")
	}
	if c.Limits.MaxStreamsPerAccount < 1 {
		add("limits.max_streams_per_account must be at least 1")
	}
	if c.Limits.MaxTranscodesTotal < 0 {
		add("limits.max_transcodes_total must not be negative")
	}
	if c.Limits.OnLimit != "replace-oldest" && c.Limits.OnLimit != "refuse" {
		add("limits.on_limit must be \"replace-oldest\" or \"refuse\"")
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
