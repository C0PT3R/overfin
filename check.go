package overfin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/jfsettings"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/oidc"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/takeover"
)

// problem is what's wrong, as a sentence for people to read: the setup
// page shows it as is, so unlike Go's usual error messages it starts with a
// capital letter.
type problem string

func (p problem) Error() string { return string(p) }

func problemf(format string, args ...any) error { return problem(fmt.Sprintf(format, args...)) }

// errCheckFailed: overfin -check found something wrong, and said what.
var errCheckFailed = errors.New("the check failed")

// checker writes one line per check: "ok" or "FAIL", then what it found,
// in plain words.
type checker struct {
	w      io.Writer
	failed bool
}

func (c *checker) ok(format string, args ...any) {
	fmt.Fprintf(c.w, "ok    "+format+"\n", args...)
}

func (c *checker) fail(format string, args ...any) {
	c.failed = true
	fmt.Fprintf(c.w, "FAIL  "+format+"\n", args...)
}

// checkSetup tests the configuration and what it points to: the secret key
// and the database, Jellyfin and its key, the identity provider, the
// connector. It changes nothing: the database is only read, and Overfin
// isn't started.
func checkSetup(configPath string, getenv func(string) string, w io.Writer) error {
	c := &checker{w: w}
	cfg, err := config.Load(configPath, getenv)
	if err != nil {
		// Every problem with the file is listed, one per line.
		for line := range strings.SplitSeq(err.Error(), "\n") {
			c.fail("%s", strings.TrimPrefix(line, "config: "))
		}
		return errCheckFailed
	}
	if configPath == "" {
		c.ok("configuration: none, the defaults")
	} else {
		c.ok("configuration: %s", configPath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c.storage(ctx, cfg)
	c.jellyfin(ctx, cfg)
	c.identity(ctx, cfg)
	if c.failed {
		return errCheckFailed
	}
	return nil
}

// storage checks the secret key against the database, and that Overfin can
// write where it needs to.
func (c *checker) storage(ctx context.Context, cfg *config.Config) {
	path := cfg.Storage.Path
	_, err := os.Stat(path)
	newDB := errors.Is(err, fs.ErrNotExist)
	switch {
	case err != nil && !newDB:
		c.fail("database %s: %v", path, err)
		return
	case cfg.Secrets.Key == nil && newDB:
		c.ok("secret key: none yet; Overfin generates it on its first start, in %s", config.KeyFile)
		c.writable(filepath.Dir(config.KeyFile), "the secret key's folder")
	case cfg.Secrets.Key == nil:
		c.fail("secret key: OVERFIN_SECRET_KEY isn't set and %s doesn't exist, but the database does: "+
			"restore the key from your backup (or, if it's lost for good, run overfin -recover-tokens once)", config.KeyFile)
	case newDB:
		c.ok("secret key: set")
	default:
		k, err := keys.Derive(cfg.Secrets.Key)
		if err != nil {
			c.fail("secret key: %v", err)
			break
		}
		match, err := store.KeyMatches(ctx, path, k.Check)
		switch {
		case err != nil:
			c.fail("secret key: %v", err)
		case !match:
			c.fail("secret key: it isn't the key this database was created with: restore the original one " +
				"(or, if it's lost for good, run overfin -recover-tokens once)")
		default:
			c.ok("secret key: it's the one this database was created with")
		}
	}
	if newDB {
		c.ok("database: none yet; Overfin creates it in %s", path)
	} else {
		c.ok("database: %s", path)
	}
	c.writable(filepath.Dir(path), "the database's folder")
}

// writable tells whether Overfin may create files in dir, by creating one
// and removing it.
func (c *checker) writable(dir, what string) {
	f, err := os.CreateTemp(dir, ".overfin-check-*")
	if err != nil {
		c.fail("%s (%s): Overfin can't write there: %v", what, dir, err)
		return
	}
	f.Close()
	os.Remove(f.Name())
}

func (c *checker) jellyfin(ctx context.Context, cfg *config.Config) {
	// The address in the configuration, else as saved; the key Overfin
	// made, as saved.
	url, key := cfg.Jellyfin.URL, ""
	var box *seal.Box
	if cfg.Secrets.Key != nil && fileExists(cfg.Storage.Path) {
		k, err := keys.Derive(cfg.Secrets.Key)
		if err != nil {
			return // already said, with the secret key
		}
		if box, err = seal.New(k.TokenEncryption); err != nil {
			c.fail("Jellyfin: %v", err)
			return
		}
		savedURL, savedKey, _, err := store.ReadJellyfin(ctx, cfg.Storage.Path, box)
		if err != nil {
			c.fail("Jellyfin: %v", err)
			return
		}
		url, key = cmp.Or(url, savedURL), savedKey
	}
	if url == "" {
		c.ok("Jellyfin: not set up yet; the setup page asks for it")
		return
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		c.fail("Jellyfin's address: %v", err)
		return
	}
	found, err := takeover.Look(ctx, jf, key, "")
	switch {
	case err != nil:
		c.fail("%v", err)
	case found.State == takeover.Fresh:
		c.ok("Jellyfin %s, at %s: fresh; Overfin sets it up when it starts", found.Server.Version, url)
	case found.State == takeover.Other:
		c.fail("Jellyfin %s, at %s: set up by someone else, or Overfin's key for it is gone; "+
			"Overfin takes it only if it's handed over, on the setup page", found.Server.Version, url)
	default:
		found, err := testJellyfin(ctx, url, key, "")
		if err != nil {
			c.fail("%v", err)
			return
		}
		c.ok("%s", found.summary)
		family, device := "", ""
		if box != nil {
			family, device, _ = store.ReadTranscoding(ctx, cfg.Storage.Path, box)
		}
		changes, err := jfsettings.Plan(ctx, jf, jellyfin.Auth{Token: key, DeviceID: "overfin-check"}, transcodingSettings(family, device)...)
		switch {
		case err != nil:
			c.fail("Jellyfin's settings can't be read: %v", err)
		case len(changes) > 0:
			lines := make([]string, len(changes))
			for i, ch := range changes {
				lines[i] = "\n        " + ch.String()
			}
			c.ok("Jellyfin settings: %d differ from Overfin's, and are set back when it starts:%s", len(changes), strings.Join(lines, ""))
		}
	}
}

// jellyfinFound is what testJellyfin found: what to tell, and the server's
// ID, which stays the same whatever address it's reached at.
type jellyfinFound struct {
	summary  string
	serverID string
}

// testJellyfin tells whether the Jellyfin server at url is Overfin's own:
// reachable, version 12, the server serverID ("" when unknown), answering
// to key, which it's sent only then. Its error says what's wrong in plain
// words; the control panel shows it as is.
func testJellyfin(ctx context.Context, url, key, serverID string) (jellyfinFound, error) {
	if err := config.CheckJellyfinURL(url); err != nil {
		return jellyfinFound{}, problemf("Jellyfin's address %s", err)
	}
	jf, err := jellyfin.New(url, Version)
	if err != nil {
		return jellyfinFound{}, problemf("Jellyfin's address: %v", err)
	}
	found, err := takeover.Look(ctx, jf, key, serverID)
	if err != nil {
		return jellyfinFound{}, err
	}
	if found.State != takeover.Ours {
		return jellyfinFound{}, problemf("Jellyfin at %s isn't Overfin's: that's another Jellyfin server. Only a new address for the same one can be set here", url)
	}
	n, err := jf.CountLibraries(ctx, jellyfin.Auth{Token: key, DeviceID: "overfin-check"})
	if err != nil {
		return jellyfinFound{}, problemf("Jellyfin: %v", err)
	}
	libraries := "libraries"
	if n == 1 {
		libraries = "library"
	}
	return jellyfinFound{summary: fmt.Sprintf("Jellyfin %s, at %s: Overfin's own; %d %s", found.Server.Version, url, n, libraries),
		serverID: found.Server.ID}, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (c *checker) identity(ctx context.Context, cfg *config.Config) {
	if cfg.OIDC.Enabled() {
		p := &oidc.Provider{Issuer: cfg.OIDC.Issuer, ClientID: cfg.OIDC.ClientID, HTTP: oidc.NewClient(cfg.OIDC.Dial)}
		if err := p.Check(ctx); err != nil {
			c.fail("identity provider (%s): %v", cfg.OIDC.Issuer, err)
		} else {
			c.ok("identity provider: %s", cfg.OIDC.Issuer)
		}
	}
	if cfg.Identity.Mode == "forwardauth" {
		c.ok("identity: forward-auth, trusting %s; only your reverse proxy may reach Overfin",
			strings.Join(cfg.Identity.TrustedProxies, ", "))
	}
	if cn := cfg.Connector; cn.Enabled() {
		a := connector.New(cn.Kind, cn.URL, cfg.Secrets.ConnectorToken, oidc.NewClient(cn.Dial))
		switch err := a.Check(ctx); {
		case connector.Refused(err):
			c.fail("connector (%s): its key (OVERFIN_CONNECTOR_TOKEN) is refused, or may not view users", cn.URL)
		case err != nil:
			c.fail("connector (%s): %v", cn.URL, err)
		default:
			c.ok("connector: %s", cn.URL)
		}
	}
}
