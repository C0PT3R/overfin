// Package provision creates an account, its owner profile and a matching
// Jellyfin user the first time someone logs in.
//
// Jellyfin users get opaque names and random passwords nobody ever sees
// (SECURITY.md rule 47); the password is used once to obtain the user's own
// token, then forgotten. Only identities in the required group are
// provisioned, at a limited rate (rule 15). A failure after the Jellyfin user
// exists deletes that user again, so nothing is left behind.
package provision

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/store"
)

var (
	// ErrNotAllowed: the identity isn't in the required group, or its
	// account is disabled.
	ErrNotAllowed = errors.New("provision: not allowed")
	// ErrRateLimited: too many new accounts in a short time.
	ErrRateLimited = errors.New("provision: rate limited")
	// ErrAnonymous: anonymous visitors have no account.
	ErrAnonymous = errors.New("provision: anonymous visitors have no account")
)

// OwnerProfileName is the display name of a new account's first profile.
// Users rename it later.
const OwnerProfileName = "Main"

// Provisioner hands out accounts.
type Provisioner struct {
	Store         *store.Store
	Jellyfin      *jellyfin.Client
	AdminKey      string
	RequiredGroup string
	LogKey        []byte // keys.LogID, for pseudonymous log entries
	Logger        *slog.Logger

	// mu serializes account creation. It is rare, and serializing it rules
	// out two Jellyfin users being created for one person.
	mu      sync.Mutex
	limiter bucket
}

// Ensure returns the account and owner profile for id, creating them on the
// first visit.
func (p *Provisioner) Ensure(ctx context.Context, id identity.Identity) (*store.Account, *store.Profile, error) {
	if id.Anonymous {
		return nil, nil, ErrAnonymous
	}
	if acct, prof, err := p.existing(ctx, id.Subject); !errors.Is(err, store.ErrNotFound) {
		return acct, prof, err
	}

	if p.RequiredGroup != "" && !slices.Contains(id.Groups, p.RequiredGroup) {
		p.Logger.Info("provisioning refused: not in required group", "user", p.logID(id.Subject))
		return nil, nil, ErrNotAllowed
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Someone else may have created it while we waited for the lock.
	if acct, prof, err := p.existing(ctx, id.Subject); !errors.Is(err, store.ErrNotFound) {
		return acct, prof, err
	}
	if !p.limiter.allow(time.Now()) {
		return nil, nil, ErrRateLimited
	}
	return p.create(ctx, id.Subject)
}

func (p *Provisioner) existing(ctx context.Context, subject string) (*store.Account, *store.Profile, error) {
	acct, err := p.Store.AccountBySubject(ctx, subject)
	if err != nil {
		return nil, nil, err
	}
	if acct.Disabled {
		return nil, nil, ErrNotAllowed
	}
	profiles, err := p.Store.Profiles(ctx, acct.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(profiles) == 0 || !profiles[0].Owner {
		return nil, nil, fmt.Errorf("provision: account %s has no owner profile", acct.ID)
	}
	return acct, profiles[0], nil
}

func (p *Provisioner) create(ctx context.Context, subject string) (_ *store.Account, _ *store.Profile, err error) {
	username := "of-" + randomName()
	password := randomSecret()
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	device := "overfin-" + username // one stable device per profile

	userID, err := p.Jellyfin.CreateUser(ctx, admin, username, password)
	if err != nil {
		return nil, nil, err
	}
	// From here on, any failure deletes the Jellyfin user again.
	defer func() {
		if err != nil {
			p.rollback(userID)
		}
	}()

	policy, err := p.Jellyfin.UserPolicy(ctx, admin, userID)
	if err != nil {
		return nil, nil, err
	}
	policy.LockDown()
	if err = p.Jellyfin.SetUserPolicy(ctx, admin, userID, policy); err != nil {
		return nil, nil, err
	}

	token, authID, err := p.Jellyfin.Authenticate(ctx, device, username, password)
	if err != nil {
		return nil, nil, err
	}
	if authID != userID {
		return nil, nil, errors.New("provision: logged in as a different user than created")
	}

	acct, prof, err := p.Store.CreateAccount(ctx, subject, store.NewOwner{
		Name: OwnerProfileName, JellyfinUserID: userID, JellyfinUsername: username, JellyfinToken: token,
	})
	if err != nil {
		return nil, nil, err
	}
	p.Logger.Info("account provisioned", "user", p.logID(subject), "account", acct.ID)
	return acct, prof, nil
}

// rollback deletes a Jellyfin user created by a failed attempt. It uses its
// own context, so a cancelled request still cleans up.
func (p *Provisioner) rollback(userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	if err := p.Jellyfin.DeleteUser(ctx, admin, userID); err != nil {
		p.Logger.Error("could not delete Jellyfin user after failed provisioning", "err", err)
	}
}

// logID is a keyed hash of a subject, so logs can correlate events without
// naming anyone (SECURITY.md rule 54).
func (p *Provisioner) logID(subject string) string {
	h := hmac.New(sha256.New, p.LogKey)
	h.Write([]byte(subject))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

var nameEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// randomName is 60 random bits as 12 lowercase characters.
func randomName() string {
	b := make([]byte, 8)
	rand.Read(b)
	return strings.ToLower(nameEncoding.EncodeToString(b))[:12]
}

// randomSecret is a 256-bit password, used once.
func randomSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ---- Rate limit ---------------------------------------------------------------

// bucket allows bursts of up to burst new accounts, refilling one every
// interval. The zero value uses the defaults.
type bucket struct {
	tokens float64
	last   time.Time
}

const (
	burst    = 10
	interval = 6 * time.Second
)

func (b *bucket) allow(now time.Time) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()/interval.Seconds())
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
