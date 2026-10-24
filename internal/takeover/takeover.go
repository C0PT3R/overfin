// Package takeover tells what a Jellyfin server is to Overfin, and makes it
// Overfin's own: a fresh one by running its first-run setup, any other only
// when the operator hands it over with an administrator's login. Either way
// Overfin then works with a key it made for itself. It never changes
// anything on a server it doesn't take (SECURITY.md rule 67).
package takeover

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/overfin/server/internal/jellyfin"
)

// AppName is the name of Overfin's key in Jellyfin's list of API keys.
const AppName = "Overfin"

// AdminName is the administrator Overfin gives a fresh Jellyfin. Its
// password is random and forgotten at once.
const AdminName = "overfin-admin"

// deviceID is how the takeover's own logins show in Jellyfin.
const deviceID = "overfin-takeover"

// Problem is what's wrong, as a sentence for people to read: pages show it
// as is, so unlike Go's usual error messages it starts with a capital
// letter.
type Problem string

func (p Problem) Error() string { return string(p) }

// The answers about an administrator's login.
const (
	ErrLogin    Problem = "Jellyfin refuses that username and password."
	ErrNotAdmin Problem = "That account isn't one of this Jellyfin's administrators."
)

// State is what a Jellyfin server is to Overfin.
type State int

const (
	// Fresh: its first-run setup never ran. Overfin may run it.
	Fresh State = iota
	// Ours: it answers to the key Overfin made on it.
	Ours
	// Other: set up by someone else, or Overfin lost its key. Only the
	// operator can hand it over.
	Other
)

// Found is what Look found.
type Found struct {
	State  State
	Server jellyfin.Server
}

// Look tells what the Jellyfin server behind jf is to Overfin. key is the
// key Overfin keeps for it, or "" when it has none, and serverID the server
// that key was made on ("" when unknown). Its errors are Problems.
func Look(ctx context.Context, jf *jellyfin.Client, key, serverID string) (Found, error) {
	s, err := jf.Public(ctx)
	if err != nil {
		return Found{}, Problem(fmt.Sprintf("Jellyfin can't be reached (%v). It's reached from where Overfin runs: "+
			"in a container, localhost is the container itself.", err))
	}
	if !strings.HasPrefix(s.Version, "12.") {
		return Found{}, Problem(fmt.Sprintf("Jellyfin runs version %s, and Overfin needs Jellyfin 12.", s.Version))
	}
	if !s.SetUp {
		return Found{Fresh, s}, nil
	}
	if key == "" || (serverID != "" && serverID != s.ID) {
		return Found{Other, s}, nil
	}
	_, err = jf.CountLibraries(ctx, jellyfin.Auth{Token: key, DeviceID: "overfin-check"})
	switch {
	case err == nil:
		return Found{Ours, s}, nil
	case jellyfin.IsStatus(err, http.StatusUnauthorized), jellyfin.IsStatus(err, http.StatusForbidden):
		return Found{Other, s}, nil
	default:
		return Found{}, Problem(fmt.Sprintf("Jellyfin answered, but not as expected (%v).", err))
	}
}

// SetUp runs a fresh Jellyfin's first-run setup, with an administrator
// whose password nobody keeps, and returns the key Overfin made with it.
func SetUp(ctx context.Context, jf *jellyfin.Client, st jellyfin.Startup) (key string, err error) {
	password := rand.Text()
	if err := jf.RunStartup(ctx, st, AdminName, password); err != nil {
		return "", fmt.Errorf("takeover: setting Jellyfin up: %w", err)
	}
	// From now on, only this password opens the server: the key is made
	// even if whoever asked went away, and tried again a few times, or the
	// server would be lost to Overfin.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keyTries*time.Minute)
	defer cancel()
	for try := 1; ; try++ {
		key, err = withAdmin(ctx, jf, AdminName, password, func(a jellyfin.Auth) (string, error) {
			return jf.CreateKey(ctx, a, AppName)
		})
		if err == nil || try == keyTries {
			return key, err
		}
		select {
		case <-ctx.Done():
			return "", err
		case <-time.After(time.Duration(try) * keyWait):
		}
	}
}

// keyTries is how many times SetUp tries to make its key, keyWait more
// between each.
const keyTries = 5

var keyWait = 2 * time.Second

// Survey is what a Jellyfin server holds, for the operator to see before
// handing it over.
type Survey struct {
	Users     []string
	Libraries int
}

// Inspect looks over a Jellyfin server with an administrator's login, changing
// nothing: its users and how many libraries it has.
func Inspect(ctx context.Context, jf *jellyfin.Client, username, password string) (Survey, error) {
	var s Survey
	_, err := withAdmin(ctx, jf, username, password, func(a jellyfin.Auth) (string, error) {
		var err error
		if s.Users, err = jf.UserNames(ctx, a); err != nil {
			return "", err
		}
		s.Libraries, err = jf.CountLibraries(ctx, a)
		return "", err
	})
	return s, err
}

// HandOver makes Overfin's key on a server the operator hands over, with an
// administrator's login used for this alone.
func HandOver(ctx context.Context, jf *jellyfin.Client, username, password string) (key string, err error) {
	return withAdmin(ctx, jf, username, password, func(a jellyfin.Auth) (string, error) {
		return jf.CreateKey(ctx, a, AppName)
	})
}

// withAdmin signs in as an administrator, runs do, and signs out again.
func withAdmin(ctx context.Context, jf *jellyfin.Client, username, password string, do func(jellyfin.Auth) (string, error)) (string, error) {
	token, _, err := jf.Authenticate(ctx, deviceID, username, password)
	if jellyfin.IsStatus(err, http.StatusUnauthorized) {
		return "", ErrLogin
	}
	if err != nil {
		return "", fmt.Errorf("takeover: signing in: %w", err)
	}
	a := jellyfin.Auth{Token: token, DeviceID: deviceID}
	defer jf.Logout(context.WithoutCancel(ctx), a)
	if _, admin, err := jf.Me(ctx, a); err != nil {
		return "", fmt.Errorf("takeover: %w", err)
	} else if !admin {
		return "", ErrNotAdmin
	}
	return do(a)
}

// CloseAccess gives every administrator of the server a password nobody
// knows, so nobody signs in to Jellyfin itself; Overfin works with its key.
// Other users and other apps' keys stay as they are.
func CloseAccess(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth) error {
	users, err := jf.Users(ctx, admin)
	if err != nil {
		return err
	}
	for _, u := range users {
		if !u.Admin {
			continue
		}
		if err := jf.SetPassword(ctx, admin, u.ID, rand.Text()); err != nil {
			return fmt.Errorf("takeover: closing %s: %w", u.Name, err)
		}
	}
	return nil
}

// OpenAccess gives Overfin's administrator (AdminName, made if it's
// missing) a new password, and returns it: shown once, never kept.
func OpenAccess(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth) (name, password string, err error) {
	users, err := jf.Users(ctx, admin)
	if err != nil {
		return "", "", err
	}
	password = rand.Text()
	for _, u := range users {
		if u.Name == AdminName {
			if !u.Admin {
				return "", "", Problem("Jellyfin has a user named " + AdminName + " who isn't an administrator.")
			}
			return AdminName, password, jf.SetPassword(ctx, admin, u.ID, password)
		}
	}
	id, err := jf.CreateUser(ctx, admin, AdminName, password)
	if err != nil {
		return "", "", err
	}
	policy, err := jf.UserPolicy(ctx, admin, id)
	if err == nil {
		policy["IsAdministrator"] = true
		err = jf.SetUserPolicy(ctx, admin, id, policy)
	}
	if err != nil {
		// Not left half made: a user of that name who isn't an
		// administrator would stop every later try.
		if derr := jf.DeleteUser(context.WithoutCancel(ctx), admin, id); derr != nil {
			return "", "", fmt.Errorf("%w (and removing %s again: %v)", err, AdminName, derr)
		}
		return "", "", err
	}
	return AdminName, password, nil
}

// Tidy revokes the keys Overfin made on earlier takeovers, keeping key, and
// says how many it revoked.
func Tidy(ctx context.Context, jf *jellyfin.Client, admin jellyfin.Auth, key string) (int, error) {
	keys, err := jf.KeysOf(ctx, admin, AppName)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, k := range keys {
		if k == key {
			continue
		}
		if err := jf.DeleteKey(ctx, admin, k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
