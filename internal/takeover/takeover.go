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
	return withAdmin(ctx, jf, AdminName, password, func(a jellyfin.Auth) (string, error) {
		return jf.CreateKey(ctx, a, AppName)
	})
}

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
