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
	"strings"
	"sync"
	"time"

	"github.com/overfin/server/internal/identity"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

var (
	// ErrNotAllowed: the identity isn't in the required group, or its
	// account is disabled.
	ErrNotAllowed = errors.New("provision: not allowed")
	// ErrExpired: the operator set an end to the account's access, and it
	// has passed.
	ErrExpired = errors.New("provision: account expired")
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
	// DeleteSeerrUser, when set, deletes a deleted account's Seerr user
	// (by its Seerr ID); nil when no Seerr is connected.
	DeleteSeerrUser func(ctx context.Context, seerrID int) error
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
	if err != nil {
		return nil, nil, err
	}
	// The required group is checked on every request, not only at
	// creation: someone taken out of it is out at once (rule 16).
	if p.RequiredGroup != "" && !slices.Contains(id.Groups, p.RequiredGroup) {
		return nil, nil, ErrNotAllowed
	}
	return acct, prof, nil
}

// Hooks give modules a say in creating an account. Both run under the
// creation lock. Before runs when nothing is created yet; an error refuses.
// After runs once the account exists; an error undoes it.
type Hooks struct {
	Before func(ctx context.Context) error
	After  func(ctx context.Context, acct *store.Account, owner *store.Profile) error
}

func (h *Hooks) before(ctx context.Context) error {
	if h == nil || h.Before == nil {
		return nil
	}
	return h.Before(ctx)
}

func (h *Hooks) after(ctx context.Context, acct *store.Account, owner *store.Profile) error {
	if h == nil || h.After == nil {
		return nil
	}
	return h.After(ctx, acct, owner)
}

// Ensure returns the account for id, creating a fresh one on the first
// visit.
func (p *Provisioner) Ensure(ctx context.Context, id identity.Identity, hooks *Hooks) (*store.Account, *store.Profile, error) {
	acct, prof, err := p.Find(ctx, id)
	if !errors.Is(err, ErrNoAccount) {
		return acct, prof, err
	}
	acct, prof, err = p.Create(ctx, id, hooks)
	if errors.Is(err, ErrAccountExists) {
		// Another request created it in the meantime; that's the one.
		return p.Find(ctx, id)
	}
	return acct, prof, err
}

// Create makes a fresh account with a new Jellyfin user, or returns
// ErrAccountExists.
func (p *Provisioner) Create(ctx context.Context, id identity.Identity, hooks *Hooks) (*store.Account, *store.Profile, error) {
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
	if err := hooks.before(ctx); err != nil {
		return nil, nil, err
	}
	acct, owner, err := p.create(ctx, id.Subject)
	if err != nil {
		return nil, nil, err
	}
	if err := hooks.after(ctx, acct, owner); err != nil {
		// Undone whole: the account and its Jellyfin users go.
		if _, derr := p.DeleteAccount(ctx, acct.ID, false); derr != nil {
			p.Logger.Error("undoing an account failed", "account", acct.ID, "err", derr)
		}
		return nil, nil, err
	}
	return acct, owner, nil
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
//
// With move, claiming is one-way: the Jellyfin user gets an opaque name, the
// lock-down and a password nobody knows, so its old login stops working.
// Otherwise it keeps its name and password until moved later (link).
func (p *Provisioner) Claim(ctx context.Context, id identity.Identity, username, password string, move bool, hooks *Hooks) (*store.Account, *store.Profile, error) {
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
	// Modules have their say first (an invitation, say), before the old
	// account is even looked at.
	if err := hooks.before(ctx); err != nil {
		return nil, nil, err
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

	// The check's login was on a throwaway device; end it, so its token
	// doesn't linger in Jellyfin.
	if err := p.Jellyfin.Logout(ctx, jellyfin.Auth{Token: token, DeviceID: "overfin-claim"}); err != nil {
		p.Logger.Warn("could not end the claim check's session", "err", err)
	}

	var undo func()
	name := username
	if move {
		if name, token, undo, err = p.move(ctx, userID, password); err != nil {
			return nil, nil, err
		}
	} else if token, _, err = p.Jellyfin.Authenticate(ctx, DeviceID(userID), username, password); err != nil {
		return nil, nil, err
	}

	acct, prof, err := p.Store.CreateAccount(ctx, id.Subject, store.NewOwner{
		Name: OwnerProfileName, JellyfinUserID: userID, JellyfinUsername: name, JellyfinToken: token,
	})
	if err != nil {
		if undo != nil {
			undo()
		}
		return nil, nil, err
	}
	if err := hooks.after(ctx, acct, prof); err != nil {
		// Undone: the account goes, and the old Jellyfin account is put
		// back as it was. It's someone's real account: never deleted.
		if derr := p.Store.DeleteAccount(ctx, acct.ID); derr != nil {
			p.Logger.Error("undoing an adopted account failed", "account", acct.ID, "err", derr)
		}
		if undo != nil {
			undo()
		}
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
	if acct.Expired(time.Now()) {
		return nil, nil, ErrExpired
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

// create makes a new account. It needs no Jellyfin user of its own:
// Overfin's one Jellyfin account serves everyone.
func (p *Provisioner) create(ctx context.Context, subject string) (*store.Account, *store.Profile, error) {
	acct, prof, err := p.Store.CreateAccount(ctx, subject, store.NewOwner{Name: OwnerProfileName})
	if err != nil {
		return nil, nil, err
	}
	p.Logger.Info("account provisioned", "user", p.logID(subject), "account", acct.ID)
	return acct, prof, nil
}

// AddProfile creates another profile in an account. It needs no Jellyfin
// user of its own, and a kids profile's ceilings are Overfin's to apply.
func (p *Provisioner) AddProfile(ctx context.Context, accountID string, np store.NewProfile, maxProfiles int) (*store.Profile, error) {
	return p.Store.AddProfile(ctx, accountID, np, maxProfiles)
}

// Unrestrict lifts any rating limit from a profile's Jellyfin user: Overfin
// holds kids profiles to its own ratings, and Jellyfin's would hide
// titles Overfin allows.
func (p *Provisioner) Unrestrict(ctx context.Context, jellyfinUserID string) error {
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, jellyfinUserID)
	if err != nil {
		return err
	}
	if !policy.Limited() {
		return nil
	}
	policy.Unlimited()
	return p.Jellyfin.SetUserPolicy(ctx, admin, jellyfinUserID, policy)
}

// move takes an existing Jellyfin user over for good: the lock-down, an
// opaque name, and a password nobody knows, then a fresh token. Its old
// login stops working. With the old password (when claiming), every step
// that fails is undone, and the returned undo puts everything back if what
// follows fails; without it (moving later), there's nothing to restore it to.
func (p *Provisioner) move(ctx context.Context, userID, oldPassword string) (name, token string, undo func(), err error) {
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, userID)
	if err != nil {
		return "", "", nil, err
	}
	original := jellyfin.Policy{}
	for k, v := range policy {
		original[k] = v
	}
	user, err := p.Jellyfin.UserName(ctx, admin, userID)
	if err != nil {
		return "", "", nil, err
	}

	// Steps done so far, undone in reverse on failure.
	var done []func()
	undo = func() {
		for i := len(done) - 1; i >= 0; i-- {
			done[i]()
		}
	}
	fail := func(e error) (string, string, func(), error) {
		undo()
		return "", "", nil, e
	}

	policy.LockDown()
	if err := p.Jellyfin.SetUserPolicy(ctx, admin, userID, policy); err != nil {
		return fail(err)
	}
	done = append(done, func() { _ = p.Jellyfin.SetUserPolicy(context.Background(), admin, userID, original) })

	name = "of-" + randomName()
	if err := p.Jellyfin.RenameUser(ctx, admin, userID, name); err != nil {
		return fail(err)
	}
	done = append(done, func() { _ = p.Jellyfin.RenameUser(context.Background(), admin, userID, user) })

	password := randomSecret()
	if err := p.Jellyfin.SetPassword(ctx, admin, userID, password); err != nil {
		return fail(err)
	}
	if oldPassword != "" {
		done = append(done, func() { _ = p.Jellyfin.SetPassword(context.Background(), admin, userID, oldPassword) })
	}

	if token, _, err = p.Jellyfin.Authenticate(ctx, DeviceID(userID), name, password); err != nil {
		return fail(err)
	}
	return name, token, undo, nil
}

// MoveLinked moves every account adopted in "link" mode for good, as
// claiming in "move" mode would have: for when the operator closes the old
// way to Jellyfin. Their owners' old logins stop working. It returns how
// many moved; the others are left for a retry.
func (p *Provisioner) MoveLinked(ctx context.Context) (moved int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	linked, err := p.Store.LinkedProfiles(ctx)
	if err != nil {
		return 0, err
	}
	failed := 0
	for _, l := range linked {
		name, token, _, err := p.move(ctx, l.JellyfinUserID, "")
		if err == nil {
			err = p.Store.SetJellyfinUser(ctx, l.ProfileID, name, token)
		}
		if err != nil {
			failed++
			p.Logger.Error("linked account not moved", "profile", l.ProfileID, "err", err)
			continue
		}
		moved++
		p.Logger.Info("linked account moved", "profile", l.ProfileID)
	}
	if failed > 0 {
		return moved, fmt.Errorf("provision: %d linked accounts not moved", failed)
	}
	return moved, nil
}

// Relogin gives a profile's Jellyfin user a new random password and logs in
// with it, returning a fresh token: for recovering after the secret key was
// lost. Only for users Overfin created; an adopted user's password is its
// owner's, and is never touched.
func (p *Provisioner) Relogin(ctx context.Context, jellyfinUserID, username string) (string, error) {
	if !strings.HasPrefix(username, "of-") {
		return "", fmt.Errorf("provision: %s was adopted, not created by Overfin", username)
	}
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	password := randomSecret()
	if err := p.Jellyfin.SetPassword(ctx, admin, jellyfinUserID, password); err != nil {
		return "", err
	}
	// Same device ID as before: Jellyfin replaces the old session.
	token, _, err := p.Jellyfin.Authenticate(ctx, DeviceID(jellyfinUserID), username, password)
	return token, err
}

// DeleteAccount deletes an account and everything Overfin holds about it.
// Its Jellyfin users go first, deleted with their history or disabled
// (keepHistory), and its Seerr user, deleted with its requests (kept with
// keepHistory: it's anonymous, and no one can sign in as it), so a failure
// deletes nothing here and a retry picks up where it stopped. It returns
// the account's profiles, as they were.
func (p *Provisioner) DeleteAccount(ctx context.Context, accountID string, keepHistory bool) ([]*store.Profile, error) {
	profiles, err := p.Store.Profiles(ctx, accountID)
	if err != nil {
		return nil, err
	}
	for _, pr := range profiles {
		if pr.JellyfinUserID == "" {
			continue // none of its own
		}
		if keepHistory {
			err = p.DisableJellyfinUser(ctx, pr.JellyfinUserID)
		} else {
			err = p.DeleteJellyfinUser(ctx, pr.JellyfinUserID)
		}
		if err != nil {
			return nil, err
		}
	}
	if !keepHistory && p.DeleteSeerrUser != nil {
		if id, ok, err := p.Store.SeerrUser(ctx, accountID); err != nil {
			return nil, err
		} else if ok {
			if err := p.DeleteSeerrUser(ctx, id); err != nil {
				return nil, err
			}
		}
	}
	if err := p.Store.DeleteAccount(ctx, accountID); err != nil {
		return nil, err
	}
	p.Logger.Info("account deleted", "account", accountID)
	return profiles, nil
}

// DisableJellyfinUser disables a profile's Jellyfin user, keeping its
// history on the server: for operators who keep it when an account is
// deleted.
func (p *Provisioner) DisableJellyfinUser(ctx context.Context, jellyfinUserID string) error {
	if jellyfinUserID == "" {
		return nil // a profile without one
	}
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	policy, err := p.Jellyfin.UserPolicy(ctx, admin, jellyfinUserID)
	if jellyfin.IsStatus(err, http.StatusNotFound) {
		return nil // already gone
	}
	if err != nil {
		return err
	}
	policy["IsDisabled"] = true
	return p.Jellyfin.SetUserPolicy(ctx, admin, jellyfinUserID, policy)
}

// DeleteJellyfinUser deletes a profile's Jellyfin user, with its watch
// history. A user already gone counts as deleted.
func (p *Provisioner) DeleteJellyfinUser(ctx context.Context, jellyfinUserID string) error {
	if jellyfinUserID == "" {
		return nil // a profile without one
	}
	admin := jellyfin.Auth{Token: p.AdminKey, DeviceID: "overfin-admin"}
	if err := p.Jellyfin.DeleteUser(ctx, admin, jellyfinUserID); err != nil && !jellyfin.IsStatus(err, http.StatusNotFound) {
		return err
	}
	return nil
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
