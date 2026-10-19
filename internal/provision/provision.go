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
	"net/http"
	"slices"
	"sort"
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
	// ErrNoAccount: this identity has no account yet.
	ErrNoAccount = errors.New("provision: no account yet")
	// ErrAccountExists: this identity already has an account.
	ErrAccountExists = errors.New("provision: account already exists")
	// ErrClaimFailed: wrong username or password, deliberately not saying
	// which.
	ErrClaimFailed = errors.New("provision: claim failed")
	// ErrClaimNotAllowed: the credentials were right, but that Jellyfin
	// account can't be adopted (an administrator, or already adopted).
	ErrClaimNotAllowed = errors.New("provision: claim not allowed")
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

	// mu serializes account creation and adoption. Both are rare, and
	// serializing them rules out two accounts for one person.
	mu       sync.Mutex
	limiter  bucket
	attempts failures
}

// Find returns the account and owner profile for id, or ErrNoAccount.
func (p *Provisioner) Find(ctx context.Context, id identity.Identity) (*store.Account, *store.Profile, error) {
	if id.Anonymous {
		return nil, nil, ErrAnonymous
	}
	acct, prof, err := p.existing(ctx, id.Subject)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNoAccount
	}
	return acct, prof, err
}

// Ensure returns the account for id, creating a fresh one on the first
// visit.
func (p *Provisioner) Ensure(ctx context.Context, id identity.Identity) (*store.Account, *store.Profile, error) {
	acct, prof, err := p.Find(ctx, id)
	if !errors.Is(err, ErrNoAccount) {
		return acct, prof, err
	}
	acct, prof, err = p.Create(ctx, id)
	if errors.Is(err, ErrAccountExists) {
		// Another request created it in the meantime; that's the one.
		return p.Find(ctx, id)
	}
	return acct, prof, err
}

// Create makes a fresh account with a new Jellyfin user, or returns
// ErrAccountExists.
func (p *Provisioner) Create(ctx context.Context, id identity.Identity) (*store.Account, *store.Profile, error) {
	if err := p.allowed(id); err != nil {
		return nil, nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	// Checked under the lock: someone may have just created it.
	if _, _, err := p.Find(ctx, id); !errors.Is(err, ErrNoAccount) {
		if err == nil {
			return nil, nil, ErrAccountExists
		}
		return nil, nil, err
	}
	if !p.limiter.allow(time.Now()) {
		return nil, nil, ErrRateLimited
	}
	return p.create(ctx, id.Subject)
}

// allowed checks the parts of rule 15 that don't need the database.
// Allowed reports whether id may use this instance at all: not anonymous,
// and in the required group if there is one.
func (p *Provisioner) Allowed(id identity.Identity) error { return p.allowed(id) }

func (p *Provisioner) allowed(id identity.Identity) error {
	if id.Anonymous {
		return ErrAnonymous
	}
	if p.RequiredGroup != "" && !slices.Contains(id.Groups, p.RequiredGroup) {
		p.Logger.Info("provisioning refused: not in required group", "user", p.logID(id.Subject))
		return ErrNotAllowed
	}
	return nil
}

// Claim adopts an existing Jellyfin account: the username and password prove
// ownership, and the account's history comes along. The password is used for
// this one login and never stored or logged. Administrator accounts are
// never adopted, so Overfin never holds an administrator's token.
func (p *Provisioner) Claim(ctx context.Context, id identity.Identity, username, password string) (*store.Account, *store.Profile, error) {
	if err := p.allowed(id); err != nil {
		return nil, nil, err
	}
	now := time.Now()
	if !p.attempts.allow(id.Subject, now) {
		return nil, nil, ErrRateLimited
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, _, err := p.Find(ctx, id); !errors.Is(err, ErrNoAccount) {
		if err == nil {
			return nil, nil, ErrAccountExists
		}
		return nil, nil, err
	}
	if !p.limiter.allow(now) {
		return nil, nil, ErrRateLimited
	}

	// A throwaway device for the check; the kept token gets a stable one.
	token, userID, err := p.Jellyfin.Authenticate(ctx, "overfin-claim", username, password)
	if err != nil {
		if jellyfin.IsStatus(err, http.StatusUnauthorized) || jellyfin.IsStatus(err, http.StatusForbidden) ||
			jellyfin.IsStatus(err, http.StatusNotFound) || jellyfin.IsStatus(err, http.StatusBadRequest) {
			p.attempts.fail(id.Subject, now)
			p.Logger.Info("claim failed: wrong credentials", "user", p.logID(id.Subject))
			return nil, nil, ErrClaimFailed
		}
		return nil, nil, err
	}

	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, userID)
	if err != nil {
		return nil, nil, err
	}
	if isAdmin, _ := policy["IsAdministrator"].(bool); isAdmin {
		p.Logger.Warn("claim refused: administrator account", "user", p.logID(id.Subject))
		return nil, nil, ErrClaimNotAllowed
	}
	// Checked under the lock, so two people can't adopt the same account.
	// The database's unique constraints remain as a last line of defense.
	linked, err := p.Store.JellyfinUserLinked(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if linked {
		p.Logger.Warn("claim refused: account already adopted", "user", p.logID(id.Subject))
		return nil, nil, ErrClaimNotAllowed
	}

	acct, prof, err := p.Store.CreateAccount(ctx, id.Subject, store.NewOwner{
		Name: OwnerProfileName, JellyfinUserID: userID, JellyfinUsername: username, JellyfinToken: token,
	})
	if err != nil {
		return nil, nil, err
	}
	p.attempts.reset(id.Subject)
	p.Logger.Info("account adopted", "user", p.logID(id.Subject), "account", acct.ID)
	return acct, prof, nil
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

func (p *Provisioner) create(ctx context.Context, subject string) (*store.Account, *store.Profile, error) {
	u, rollback, err := p.newJellyfinUser(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	acct, prof, err := p.Store.CreateAccount(ctx, subject, store.NewOwner{
		Name: OwnerProfileName, JellyfinUserID: u.ID, JellyfinUsername: u.Username, JellyfinToken: u.Token,
	})
	if err != nil {
		rollback()
		return nil, nil, err
	}
	p.Logger.Info("account provisioned", "user", p.logID(subject), "account", acct.ID)
	return acct, prof, nil
}

// AddProfile creates another profile in an account, with its own Jellyfin
// user, so its watch history and limits are its own. A kids profile gets its
// rating ceiling in the same policy write as the lock-down: it never exists
// without its limit, even for a moment.
func (p *Provisioner) AddProfile(ctx context.Context, accountID string, np store.NewProfile, ceiling *jellyfin.Rating, maxProfiles int) (*store.Profile, error) {
	// Check first, so a refusal doesn't create and delete a Jellyfin user.
	existing, err := p.Store.Profiles(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if maxProfiles > 0 && len(existing) >= maxProfiles {
		return nil, store.ErrProfileLimit
	}
	for _, e := range existing {
		if e.Name == np.Name {
			return nil, store.ErrNameTaken
		}
	}

	var restrict func(jellyfin.Policy)
	if ceiling != nil {
		restrict = func(pol jellyfin.Policy) { pol.LimitTo(*ceiling) }
	}
	u, rollback, err := p.newJellyfinUser(ctx, restrict)
	if err != nil {
		return nil, err
	}
	np.JellyfinUserID, np.JellyfinUsername, np.JellyfinToken = u.ID, u.Username, u.Token
	prof, err := p.Store.AddProfile(ctx, accountID, np, maxProfiles)
	if err != nil {
		rollback()
		return nil, err
	}
	return prof, nil
}

type jellyfinUser struct {
	ID, Username, Token string
}

// newJellyfinUser creates a locked-down Jellyfin user with an opaque name and
// a random password, logs in once to get its token, and forgets the
// password. rollback deletes the user again, for when a later step fails.
func (p *Provisioner) newJellyfinUser(ctx context.Context, restrict func(jellyfin.Policy)) (_ jellyfinUser, rollback func(), err error) {
	username := "of-" + randomName()
	password := randomSecret()
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}

	userID, err := p.Jellyfin.CreateUser(ctx, admin, username, password)
	if err != nil {
		return jellyfinUser{}, nil, err
	}
	// From here on, any failure deletes the Jellyfin user again. The
	// deferred cleanup uses its own variable: the named result rollback is
	// set to nil by every "return …, nil, err" before deferred calls run.
	undo := func() { p.rollback(userID) }
	defer func() {
		if err != nil {
			undo()
		}
	}()

	policy, err := p.Jellyfin.UserPolicy(ctx, admin, userID)
	if err != nil {
		return jellyfinUser{}, nil, err
	}
	policy.LockDown()
	if restrict != nil {
		restrict(policy)
	}
	if err = p.Jellyfin.SetUserPolicy(ctx, admin, userID, policy); err != nil {
		return jellyfinUser{}, nil, err
	}

	// The same device ID every later call of this profile uses, so the
	// profile appears as one device in Jellyfin, not two.
	token, authID, err := p.Jellyfin.Authenticate(ctx, DeviceID(userID), username, password)
	if err != nil {
		return jellyfinUser{}, nil, err
	}
	if authID != userID {
		return jellyfinUser{}, nil, errors.New("provision: logged in as a different user than created")
	}
	return jellyfinUser{ID: userID, Username: username, Token: token}, undo, nil
}

// Restrict sets or lifts a profile's rating ceiling on its Jellyfin user;
// nil lifts it. Jellyfin enforces it from then on, for every request made
// with that profile's token.
func (p *Provisioner) Restrict(ctx context.Context, jellyfinUserID string, ceiling *jellyfin.Rating) error {
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, jellyfinUserID)
	if err != nil {
		return err
	}
	if ceiling != nil {
		policy.LimitTo(*ceiling)
	} else {
		policy.Unlimited()
	}
	return p.Jellyfin.SetUserPolicy(ctx, admin, jellyfinUserID, policy)
}

// DeleteJellyfinUser deletes a profile's Jellyfin user, with its watch
// history. A user already gone counts as deleted.
func (p *Provisioner) DeleteJellyfinUser(ctx context.Context, jellyfinUserID string) error {
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	if err := p.Jellyfin.DeleteUser(ctx, admin, jellyfinUserID); err != nil && !jellyfin.IsStatus(err, http.StatusNotFound) {
		return err
	}
	return nil
}

// Ratings lists the server's parental ratings, from least to most mature.
func (p *Provisioner) Ratings(ctx context.Context) ([]jellyfin.Rating, error) {
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	rs, err := p.Jellyfin.ParentalRatings(ctx, admin)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Score < rs[j].Score })
	return rs, nil
}

// DeviceID is the stable Jellyfin device ID of a profile's Jellyfin user.
func DeviceID(jellyfinUserID string) string {
	return "overfin-" + jellyfinUserID
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

// ---- Claim attempts -----------------------------------------------------------

// failures counts failed claims per identity: at most maxFailures within
// failureWindow, so the claim endpoint can't be used to guess passwords.
type failures struct {
	mu   sync.Mutex
	seen map[string][]time.Time
}

const (
	maxFailures   = 5
	failureWindow = 15 * time.Minute
)

func (f *failures) recent(subject string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range f.seen[subject] {
		if now.Sub(t) < failureWindow {
			keep = append(keep, t)
		}
	}
	return keep
}

func (f *failures) allow(subject string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recent(subject, now)) < maxFailures
}

func (f *failures) fail(subject string, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = make(map[string][]time.Time)
	}
	f.seen[subject] = append(f.recent(subject, now), now)
}

func (f *failures) reset(subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.seen, subject)
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
