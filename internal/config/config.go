// Package config loads and validates Overfin's configuration.
//
// Settings come from a TOML file. Secrets never do: they come from
// environment variables, or from files named by NAME_FILE variables (the
// Docker secrets convention). The secret key may also be one Overfin
// generated itself, in KeyFile. Anything unknown or invalid stops Overfin
// at startup instead of being guessed at.
package config

import (
	"cmp"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	Privacy  Privacy  `toml:"privacy"`
	Plugins  Plugins  `toml:"plugins"`
	Accounts Accounts `toml:"accounts"`
	// Modules holds the modules' own settings: module name, then setting.
	Modules map[string]map[string]string `toml:"modules"`
	// Adoption is the former [adoption] section, only read to carry it
	// over to the migration module.
	Adoption  Adoption  `toml:"adoption"`
	OIDC      OIDC      `toml:"oidc"`
	Admin     Admin     `toml:"admin"`
	Connector Connector `toml:"connector"`
	Site      Site      `toml:"site"`
	Mail      Mail      `toml:"mail"`
	HTTPS     HTTPS     `toml:"https"`

	// FeaturesInFile reports whether the file held feature settings
	// (limits, privacy, plugins, accounts, modules). Those live in the
	// database now; the file's are only read to fill an empty one.
	FeaturesInFile bool `toml:"-"`

	// Secrets, loaded from the environment, never from the file.
	Secrets Secrets `toml:"-"`
}

type Jellyfin struct {
	// URL is where Overfin reaches Jellyfin, e.g. "http://192.0.2.10:8096".
	// Optional: without it, the setup page asks for it, and the database
	// keeps it. Either way, Overfin makes its own key on that Jellyfin and
	// keeps it in the database (SECURITY.md rule 67).
	URL string `toml:"url"`
}

type Identity struct {
	// Mode is "managed" (the default: Overfin signs people in itself, with
	// their email and a code it sends, or a password), "oidc" (Overfin logs
	// users in with the [oidc] provider), "forwardauth" (a reverse proxy
	// does, and sends identity headers) or "anonymous".
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

// Adoption is the former [adoption] section: bringing existing accounts
// over is the migration module's now, and ImportAdoption carries these
// settings over to it.
type Adoption struct {
	Claim bool   `toml:"claim" json:"claim"`
	Mode  string `toml:"mode" json:"mode"`
}

// ImportAdoption carries the former [adoption] settings over to the
// migration module: switched on, in the same mode.
func (f *Features) ImportAdoption(a Adoption) {
	if !a.Claim {
		return
	}
	if !f.Plugins.Enabled("migration") {
		f.Plugins.Enable = append(f.Plugins.Enable, "migration")
	}
	if f.Modules == nil {
		f.Modules = map[string]map[string]string{}
	}
	if f.Modules["migration"] == nil {
		f.Modules["migration"] = map[string]string{}
	}
	f.Modules["migration"]["mode"] = cmp.Or(a.Mode, "move")
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
	// Enable lists the features switched on besides those always on.
	Enable []string `toml:"enable" json:"enable"`
}

// AlwaysOn are the features every Overfin has. They can't be switched off,
// and listing them in plugins.enable, as older settings do, changes
// nothing.
var AlwaysOn = []string{"library", "playback", "watchlist"}

// KnownPlugins are Overfin's features, and what each one needs on with it.
// The features built on the feature contract add themselves with
// RegisterPlugin.
var KnownPlugins = map[string][]string{
	"library":  nil,
	"playback": {"library"},
}

// PluginDescriptions say what each plugin offers, for the control panel.
var PluginDescriptions = map[string]string{
	"library":  "Libraries, items, posters, continue watching",
	"playback": "Watching, with subtitles",
}

// PluginSettings are the settings each module declares.
var PluginSettings = map[string][]Setting{}

// Setting is one of a module's settings. Without Choices, it's a switch:
// "on" or "off".
type Setting struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Hint    string   `json:"hint,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
}

// Choice is one value a setting can take.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// fallback is the setting's default: its first choice, or "off".
func (s Setting) fallback() string {
	if len(s.Choices) == 0 {
		return "off"
	}
	return s.Choices[0].Value
}

// allows reports whether v is one of the setting's values.
func (s Setting) allows(v string) bool {
	if len(s.Choices) == 0 {
		return v == "on" || v == "off"
	}
	return slices.ContainsFunc(s.Choices, func(c Choice) bool { return c.Value == v })
}

// RegisterPlugin makes a compiled-in module known to the configuration, so
// operators can switch it on and change its settings. Call it before
// loading the configuration.
func RegisterPlugin(name string, needs []string, description string, settings []Setting) {
	KnownPlugins[name] = needs
	PluginDescriptions[name] = description
	PluginSettings[name] = settings
}

// Enabled reports whether a feature is on: always, or switched on.
func (p Plugins) Enabled(name string) bool {
	return slices.Contains(AlwaysOn, name) || slices.Contains(p.Enable, name)
}

// Features are the settings the control panel switches on and off. They
// live in the database and apply at once.
type Features struct {
	Limits   Limits                       `json:"limits"`
	Privacy  Privacy                      `json:"privacy"`
	Plugins  Plugins                      `json:"plugins"`
	Accounts Accounts                     `json:"accounts"`
	Modules  map[string]map[string]string `json:"modules"`
}

// Features returns the configuration's feature settings.
func (c *Config) Features() Features {
	f := Features{Limits: c.Limits, Privacy: c.Privacy, Plugins: c.Plugins, Accounts: c.Accounts, Modules: c.Modules}
	f.Plugins.Enable = slices.Clone(f.Plugins.Enable)
	f.Modules = make(map[string]map[string]string, len(c.Modules))
	for name, values := range c.Modules {
		f.Modules[name] = maps.Clone(values)
	}
	f.ImportAdoption(c.Adoption)
	return f
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
	n.Limits, n.Privacy, n.Plugins, n.Accounts, n.Modules = f.Limits, f.Privacy, f.Plugins, f.Accounts, f.Modules
	n.Adoption = Adoption{}  // carried over into f already
	n.Identity.Proxies = nil // filled again by validate
	if err := n.validate(); err != nil {
		return nil, err
	}
	return &n, nil
}

// Admin is Overfin's control panel. With managed sign-in, it's at /admin/
// on the site's address, for admins; otherwise on its own listener, never
// on the internet (SECURITY.md rule 58), off unless Listen is set.
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
	// Private, with managed sign-in and no Listen (the panel is then at
	// /admin/ on the site's address), keeps the panel to private
	// addresses: LAN, VPN. With managed sign-in and Listen, the panel is
	// only on that listener, which also serves the sign-in.
	Private bool `toml:"private"`
}

// Connector reads people from the identity provider's own API, for the
// control panel: their names, and later invitations. Off unless Kind is set.
type Connector struct {
	// Kind is the provider: "authentik" or "pocketid".
	Kind string `toml:"kind"`
	// URL is the provider's address, e.g. "https://auth.example.org".
	URL string `toml:"url"`
	// Dial, when set, is the "host:port" to connect to instead of what DNS
	// says (TLS is still checked against the URL's name).
	Dial string `toml:"dial"`
	// FirstAdmin, with Pocket ID, is an email: when Pocket ID has nobody
	// yet, Overfin makes this person its admin, in the admin group.
	FirstAdmin string `toml:"first_admin"`
}

// emailPattern is an email Pocket ID also takes as a username: letters,
// digits, dots, hyphens and underscores, 50 characters at most.
var emailPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]*[A-Za-z0-9])?@[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?\.[A-Za-z]{2,}$`)

// HTTPS is Overfin serving HTTPS itself, with a certificate it gets from
// Let's Encrypt and renews. Off unless Domain is set; then the main listen
// address only answers Let's Encrypt's checks and sends everyone else to
// https://Domain.
type HTTPS struct {
	// Domain is the site's name, e.g. "media.example.org": it must point to
	// this server, reachable from the internet on ports 80 and 443.
	Domain string `toml:"domain"`
	// Listen is where HTTPS is served, ":8443" by default: publish it as
	// port 443 (and the main listen address as port 80).
	Listen string `toml:"listen"`
	// Email, optional, is Let's Encrypt's contact for this server.
	Email string `toml:"email"`
}

// Enabled reports whether Overfin serves HTTPS itself.
func (h HTTPS) Enabled() bool { return h.Domain != "" }

// domainPattern is a DNS name with at least two labels: letters, digits and
// hyphens.
var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// Mail is the SMTP server Overfin sends its emails through (sign-in
// codes). Off unless Host is set: people then sign in with a password.
type Mail struct {
	Host string `toml:"host" json:"host"`
	// Port defaults to 587 (465 with TLS = "tls", 25 with "none").
	Port int `toml:"port" json:"port"`
	// TLS is "starttls" (the default), "tls", or "none" (a relay on the
	// local network only: no password is sent then).
	TLS string `toml:"tls" json:"tls"`
	// From is the sender: "address" or "Name <address>".
	From string `toml:"from" json:"from"`
	// Username, when set, signs in to the SMTP server with
	// OVERFIN_MAIL_PASSWORD.
	Username string `toml:"username" json:"username"`
}

// Enabled reports whether an SMTP server is configured.
func (m Mail) Enabled() bool { return m.Host != "" }

// Check says what's wrong with an SMTP server's settings, if anything.
func (m Mail) Check() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if m.Host == "" || strings.ContainsAny(m.Host, "/: \t\r\n") && net.ParseIP(m.Host) == nil {
		add("mail.host must be the SMTP server's name or address")
	}
	if m.TLS != "starttls" && m.TLS != "tls" && m.TLS != "none" {
		add("mail.tls must be \"starttls\", \"tls\" or \"none\", got %q", m.TLS)
	}
	if m.Port < 1 || m.Port > 65535 {
		add("mail.port must be a port number")
	}
	if a, err := mail.ParseAddress(m.From); err != nil || !strings.Contains(a.Address, "@") {
		add("mail.from must be an email address, or \"Name <address>\"")
	}
	if m.TLS == "none" && m.Username != "" {
		add("mail.username needs TLS: a password is never sent unencrypted")
	}
	return errors.Join(errs...)
}

// Defaults fills in the TLS mode and the port when they're left out.
func (m *Mail) Defaults() {
	m.TLS = cmp.Or(m.TLS, "starttls")
	if m.Port == 0 {
		m.Port = cmp.Or(map[string]int{"tls": 465, "none": 25}[m.TLS], 587)
	}
}

// Enabled reports whether a connector is configured.
func (c Connector) Enabled() bool { return c.Kind != "" }

// Enabled reports whether the control panel is on.
func (a Admin) Enabled() bool { return a.Listen != "" }

// Site is what Overfin serves besides the API, at the site's root, to
// logged-in visitors only.
type Site struct {
	// Serve is "builtin" (overfin-web, Overfin's own client: the default),
	// the absolute path of a folder holding your own client, or "none" (the
	// site is served elsewhere; Overfin only answers /api/).
	Serve string `toml:"serve"`
	// CSP is the Content-Security-Policy of the site's files. The default
	// fits overfin-web; your own client may need more sources.
	CSP string `toml:"csp"`
	// SPAFallback serves index.html for paths that aren't files, as
	// single-page apps with their own routing need. The built-in client
	// always gets it.
	SPAFallback bool `toml:"spa_fallback"`
	// URL is the site's public address, e.g. "https://media.example.org".
	// Optional: with it, sign-in emails carry a link besides the code.
	URL string `toml:"url"`
}

// DefaultCSP is the site's Content-Security-Policy unless the operator sets
// one: everything from the site itself, nothing embedded elsewhere.
const DefaultCSP = "default-src 'self'; img-src 'self' data:; media-src 'self' blob:; worker-src 'self' blob:; " +
	"connect-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

type Storage struct {
	// Path is the database file.
	Path string `toml:"path"`
}

type Limits struct {
	// MaxTranscodesTotal caps playbacks that re-encode the video, on the
	// whole server; 0 means no cap. Remuxes aren't counted.
	MaxTranscodesTotal int `toml:"max_transcodes_total" json:"maxTranscodesTotal"`
	// MaxBitrateKbps caps every stream; 0 means no cap.
	MaxBitrateKbps int `toml:"max_bitrate_kbps" json:"maxBitrateKbps"`
	// PINAttempts is how many wrong PINs in a row lock a profile.
	PINAttempts int `toml:"pin_attempts" json:"pinAttempts"`
	// OnLimit is what happens when an account starts one stream too many:
	// "replace-oldest" (the default) or "refuse".
	OnLimit string `toml:"on_limit" json:"onLimit"`
}

type Secrets struct {
	// JellyfinAdminKey is the key Overfin made on its Jellyfin (SECURITY.md
	// rule 67), as the database keeps it; never from the environment. It's
	// used only for provisioning and administration (rule 3).
	JellyfinAdminKey string
	// Key encrypts tokens and keys PIN hashes, cursors and log IDs. It's
	// nil when none was given and KeyFile doesn't exist yet: Overfin then
	// generates one, on a first start only (GenerateKey).
	Key []byte
	// OIDCClientSecret is the client secret at the identity provider,
	// required only with [oidc].
	OIDCClientSecret string
	// ConnectorToken is the identity provider API token, required only
	// with [connector].
	ConnectorToken string
	// MailPassword is the SMTP server's password, with mail.username.
	MailPassword string
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
		Identity: Identity{Mode: "managed", GroupsSeparator: "|"},
		Limits: Limits{
			MaxTranscodesTotal: 4,
			PINAttempts:        5,
			OnLimit:            "replace-oldest",
		},
		OIDC:     OIDC{FreshForMinutes: 5, RecheckMinutes: 10, GroupsClaim: "groups"},
		Accounts: Accounts{OnDelete: "delete"},
		Site:     Site{Serve: "builtin", CSP: DefaultCSP},
		HTTPS:    HTTPS{Listen: ":8443"},
	}
}

// Load reads the file at path, applies defaults, loads secrets from the
// environment and validates everything. An empty path means no file: the
// defaults alone. getenv is usually os.Getenv; tests pass their own.
//
// One setting may also come from the environment, for a start without a
// file: OVERFIN_DOMAIN, the domain Overfin serves HTTPS for (the file's
// https.domain wins).
func Load(path string, getenv func(string) string) (*Config, error) {
	cfg := defaults()

	var meta toml.MetaData
	if path != "" {
		var err error
		if meta, err = toml.DecodeFile(path, &cfg); err != nil {
			return nil, fmt.Errorf("config: %w", err)
		}
	}
	if cfg.HTTPS.Domain == "" {
		cfg.HTTPS.Domain = strings.TrimSpace(getenv("OVERFIN_DOMAIN"))
	}
	if cfg.HTTPS.Enabled() {
		cfg.HTTPS.Domain = strings.ToLower(strings.TrimSuffix(cfg.HTTPS.Domain, "."))
		if cfg.Site.URL == "" {
			cfg.Site.URL = "https://" + cfg.HTTPS.Domain
		}
	}
	// A misspelled key would otherwise be silently ignored.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("config: unknown keys: %s", strings.Join(keys, ", "))
	}

	// Before managed sign-in, forwardauth was the default: a file written
	// for it must now say so, rather than quietly sign people in otherwise.
	if !meta.IsDefined("identity", "mode") && cfg.Identity.UserHeader != "" {
		return nil, errors.New("config: identity.mode is \"managed\" by default now: add mode = \"forwardauth\" to [identity]")
	}

	for _, section := range []string{"limits", "adoption", "privacy", "plugins", "accounts", "modules"} {
		if meta.IsDefined(section) {
			cfg.FeaturesInFile = true
		}
	}

	if err := loadSecrets(&cfg.Secrets, getenv); err != nil {
		return nil, err
	}
	if cfg.Connector.Enabled() {
		s, err := secret("OVERFIN_CONNECTOR_TOKEN", getenv)
		if err != nil {
			return nil, err
		}
		cfg.Secrets.ConnectorToken = s
	}
	if cfg.OIDC.Enabled() {
		s, err := secret("OVERFIN_OIDC_CLIENT_SECRET", getenv)
		if err != nil {
			return nil, err
		}
		cfg.Secrets.OIDCClientSecret = s
	}
	if cfg.Mail.Username != "" {
		s, err := secret("OVERFIN_MAIL_PASSWORD", getenv)
		if err != nil {
			return nil, err
		}
		cfg.Secrets.MailPassword = s
	}
	if m := &cfg.Mail; m.Enabled() {
		m.Defaults()
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func loadSecrets(s *Secrets, getenv func(string) string) error {
	// Overfin makes its own key on its Jellyfin now: one given is refused,
	// so nobody believes it's used.
	if getenv("OVERFIN_JELLYFIN_ADMIN_KEY") != "" || getenv("OVERFIN_JELLYFIN_ADMIN_KEY_FILE") != "" {
		return errors.New("OVERFIN_JELLYFIN_ADMIN_KEY isn't used anymore: Overfin makes its own key on its " +
			"Jellyfin. Remove it; if that Jellyfin was set up by hand, hand it over on the setup page")
	}

	encoded, err := secret("OVERFIN_SECRET_KEY", getenv)
	if errors.Is(err, errMissing) {
		// No key given: the one Overfin generated, if there's one yet.
		b, err := os.ReadFile(KeyFile)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("config: reading the secret key: %w", err)
		}
		encoded = strings.TrimSpace(string(b))
	} else if err != nil {
		return err
	}
	key, err := decodeKey(encoded)
	if err != nil {
		return err
	}
	s.Key = key
	return nil
}

// KeyFile is where Overfin keeps the secret key it generated itself, when
// none is given: its own folder (a volume of its own in Docker), apart from
// the database it protects, so a copy of the data never carries its key.
var KeyFile = "/secrets/overfin.key"

func decodeKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("config: OVERFIN_SECRET_KEY must be base64")
	}
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("config: OVERFIN_SECRET_KEY must decode to at least %d bytes", MinKeyBytes)
	}
	return key, nil
}

// GenerateKey makes a new secret key and writes it to KeyFile, readable by
// Overfin's user only. It never replaces a file already there. Callers make
// sure it's a first start: a new key can't open existing data.
func GenerateKey() ([]byte, error) {
	key := make([]byte, MinKeyBytes)
	rand.Read(key)
	f, err := os.OpenFile(KeyFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("config: creating the secret key: %w", err)
	}
	_, err = f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(KeyFile)
		return nil, fmt.Errorf("config: writing the secret key: %w", err)
	}
	return key, nil
}

// errMissing: neither NAME nor NAME_FILE is set.
var errMissing = errors.New("is required")

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
		if value == "" {
			return "", fmt.Errorf("config: %s_FILE names an empty file", name)
		}
	}
	if value == "" {
		return "", fmt.Errorf("config: %s %w", name, errMissing)
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

	// Jellyfin's address is set here, or on the setup page.
	if c.Jellyfin.URL != "" {
		if err := CheckJellyfinURL(c.Jellyfin.URL); err != nil {
			add("jellyfin.url: %v", err)
		}
	}

	switch c.Identity.Mode {
	case "managed":
		if c.OIDC.Enabled() {
			add("[oidc] is for identity.mode = \"oidc\": with managed sign-in, Overfin confirms logins itself")
		}
		if c.Identity.UserHeader != "" || c.Identity.GroupsHeader != "" {
			add("identity.user_header and groups_header are for forwardauth; managed sign-in reads no identity headers")
		}
		if c.Identity.RequiredGroup != "" {
			add("identity.required_group needs groups from an identity provider; managed sign-in has none")
		}
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
		add("identity.mode must be \"managed\", \"oidc\", \"forwardauth\" or \"anonymous\", got %q", c.Identity.Mode)
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
	c.settleModules(add)

	if a := c.Admin; c.Identity.Mode == "managed" {
		if a.Group != "" || a.RedirectURL != "" || a.TLSCert != "" || a.TLSKey != "" {
			add("with managed sign-in, [admin] takes only listen (the panel on a private listener of its own) " +
				"or private = true (the panel on the site, for private addresses only)")
		}
		if a.Listen != "" && a.Private {
			add("admin.listen and admin.private don't go together: with listen, the panel is only on its own listener, " +
				"which is always private")
		}
		if host, _, err := net.SplitHostPort(a.Listen); a.Listen != "" && err != nil {
			add("admin.listen must be host:port, e.g. \":8081\"")
		} else if ip, err := netip.ParseAddr(host); a.Listen != "" && host != "" && (err != nil || !PrivateAddr(ip)) {
			add("admin.listen must be a private address, or none (\":8081\"): the panel is never on the internet")
		}
	} else if a.Private {
		add("admin.private is for managed sign-in; the panel's own listener is always private")
	}
	if a := c.Admin; a.Enabled() && c.Identity.Mode != "managed" {
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

	switch s := c.Site; {
	case s.Serve == "builtin" || s.Serve == "none":
	case !filepath.IsAbs(s.Serve):
		add("site.serve must be \"builtin\", \"none\" or a folder's absolute path, got %q", s.Serve)
	default:
		if fi, err := os.Stat(s.Serve); err != nil || !fi.IsDir() {
			add("site.serve: %q isn't a folder Overfin can read", s.Serve)
		}
	}
	if s := c.Site.CSP; s == "" || len(s) > 2048 || strings.ContainsAny(s, "\r\n\x00") {
		add("site.csp must be a single line, 1 to 2048 characters")
	}
	if s := c.Site.URL; s != "" {
		if u, err := url.Parse(s); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			add("site.url must be the site's address alone, e.g. \"https://media.example.org\"")
		}
	}

	if h := c.HTTPS; h.Enabled() {
		if !domainPattern.MatchString(h.Domain) || len(h.Domain) > 253 {
			add("https.domain must be a domain name, e.g. \"media.example.org\", got %q", h.Domain)
		}
		if _, _, err := net.SplitHostPort(h.Listen); err != nil {
			add("https.listen must be host:port, e.g. \":8443\"")
		} else if h.Listen == c.Listen {
			add("https.listen must differ from listen, which answers Let's Encrypt's checks")
		}
		if h.Email != "" {
			if a, err := mail.ParseAddress(h.Email); err != nil || a.Address != h.Email {
				add("https.email must be an email address")
			}
		}
		if u, err := url.Parse(c.Site.URL); err == nil && u.Host != h.Domain {
			add("site.url must be https://%s, the domain Overfin serves HTTPS for", h.Domain)
		}
	}
	if m := c.Mail; m.Enabled() {
		if err := m.Check(); err != nil {
			add("%v", err)
		}
	} else if c.Mail != (Mail{}) {
		add("mail.host is required to send emails")
	}

	if cn := c.Connector; cn.Enabled() {
		if cn.Kind != "authentik" && cn.Kind != "pocketid" {
			add("connector.kind must be \"authentik\" or \"pocketid\", got %q", cn.Kind)
		}
		if u, err := url.Parse(cn.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			add("connector.url must be the provider's https:// address")
		}
		if cn.Dial != "" {
			if _, _, err := net.SplitHostPort(cn.Dial); err != nil {
				add("connector.dial must be host:port")
			}
		}
		if !c.Admin.Enabled() {
			add("the connector is used by the control panel: set up [admin] too")
		}
		if e := cn.FirstAdmin; e != "" && (cn.Kind != "pocketid" || len(e) > 50 || !emailPattern.MatchString(e)) {
			add("connector.first_admin is an email (letters, digits, dots, hyphens, underscores; 50 characters at most), with kind = \"pocketid\" only")
		}
	}

	if c.Storage.Path == "" {
		add("storage.path must not be empty")
	}

	if u := c.Identity.LogoutURL; u != "" && !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "https://") {
		add("identity.logout_url must be a path or an https:// URL")
	}
	if c.Limits.PINAttempts < 3 || c.Limits.PINAttempts > 10 {
		add("limits.pin_attempts must be between 3 and 10")
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

// CheckJellyfinURL accepts only a plain http(s) base URL: no credentials,
// query or fragment, so nothing secret or surprising hides in the config.
func CheckJellyfinURL(raw string) error {
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

// settleModules checks the modules' settings and fills in the defaults, so
// every compiled-in module has all of its settings. Those of modules this
// build doesn't have are kept as they are, for a build that does.
func (c *Config) settleModules(add func(string, ...any)) {
	settled := make(map[string]map[string]string, len(c.Modules))
	for name, values := range c.Modules {
		if _, known := PluginSettings[name]; !known {
			settled[name] = values
		}
	}
	for _, name := range slices.Sorted(maps.Keys(PluginSettings)) {
		settings := PluginSettings[name]
		values := c.Modules[name]
		for _, key := range slices.Sorted(maps.Keys(values)) {
			if !slices.ContainsFunc(settings, func(s Setting) bool { return s.Key == key }) {
				add("modules.%s: unknown setting %q", name, key)
			}
		}
		out := make(map[string]string, len(settings))
		for _, s := range settings {
			v, set := values[s.Key]
			switch {
			case !set:
				v = s.fallback()
			case !s.allows(v):
				add("modules.%s.%s: %q is not one of its values", name, s.Key, v)
			}
			out[s.Key] = v
		}
		if len(out) > 0 {
			settled[name] = out
		}
	}
	c.Modules = settled
}
