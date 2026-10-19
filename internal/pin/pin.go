// Package pin checks profile PINs.
//
// A PIN is short, so it is only as strong as the limits on guessing it. Each
// wrong PIN doubles the wait before the next attempt (1, 2, 4, 8 seconds),
// and the fifth locks the profile until the account owner logs in again
// (SECURITY.md rule 22). PINs are stored as
// keyed hashes bound to their profile: a copied database row neither reveals
// a PIN nor works on another profile.
package pin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"github.com/C0PT3R/overfin/internal/store"
)

// DefaultMaxFailures is how many wrong PINs in a row lock a profile, unless
// the operator sets another number.
const DefaultMaxFailures = 5

// maxWait caps the wait between attempts, for operators who allow many.
const maxWait = time.Minute

// forever stands for a lock only the owner's fresh login lifts.
var forever = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// Forget is how long after the last wrong PIN the count starts over.
const Forget = 15 * time.Minute

var (
	// ErrRequired: the profile has a PIN and none was given.
	ErrRequired = errors.New("pin: required")
	// ErrIncorrect: a wrong PIN.
	ErrIncorrect = errors.New("pin: incorrect")
)

// WaitError means no attempt is possible yet. Locked is true after
// MaxFailures wrong PINs, false during the short wait between attempts.
// Wait is zero for a lock that only the owner's fresh login lifts.
type WaitError struct {
	Locked bool
	Wait   time.Duration
}

func (e *WaitError) Error() string { return "pin: wait " + e.Wait.String() }

// Valid reports whether s has the shape of a PIN: 4 to 8 digits.
func Valid(s string) bool {
	if len(s) < 4 || len(s) > 8 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Checker hashes and checks PINs.
type Checker struct {
	Key   []byte
	Store *store.Store
	// UnlockAfter, when set, ends locks by themselves after that long: for
	// instances without step-up, where the owner can't confirm a fresh
	// login. Zero keeps a lock until the owner does.
	UnlockAfter time.Duration
	// MaxFailures is how many wrong PINs in a row lock a profile;
	// DefaultMaxFailures when zero.
	MaxFailures int

	// mu makes each check a whole read-decide-write step, so parallel
	// guesses can't all slip through the same wait.
	mu  sync.Mutex
	now func() time.Time
}

// Hash is the keyed hash of a profile's PIN.
func (c *Checker) Hash(profileID, pin string) []byte {
	m := hmac.New(sha256.New, c.Key)
	m.Write([]byte(profileID + ":" + pin))
	return m.Sum(nil)
}

func (c *Checker) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Check verifies pin against a profile's PIN. A profile without a PIN
// passes with any input, including none.
func (c *Checker) Check(ctx context.Context, accountID, profileID, pin string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, err := c.Store.PIN(ctx, accountID, profileID)
	if err != nil {
		return err
	}
	if st.Hash == nil {
		return nil
	}
	now := c.clock()
	if now.Before(st.LockedUntil) {
		return c.locked(st.LockedUntil, now)
	}
	failures := st.Failures
	if !st.LockedUntil.IsZero() || now.Sub(st.FailedAt) > Forget {
		failures = 0 // a lock that ran out, or old mistakes
	}
	if failures > 0 {
		if next := st.FailedAt.Add(min(time.Second<<min(failures-1, 30), maxWait)); now.Before(next) {
			return &WaitError{Wait: next.Sub(now)}
		}
	}
	if pin == "" {
		return ErrRequired
	}
	if hmac.Equal(c.Hash(profileID, pin), st.Hash) {
		if failures > 0 || !st.LockedUntil.IsZero() {
			return c.Store.RecordPIN(ctx, profileID, 0, time.Time{}, time.Time{})
		}
		return nil
	}
	failures++
	var lockedUntil time.Time
	if failures >= c.maxFailures() {
		lockedUntil = forever
		if c.UnlockAfter > 0 {
			lockedUntil = now.Add(c.UnlockAfter)
		}
	}
	// The store keeps whole seconds: round up, so a wait is never shorter
	// than it should be.
	failedAt := now.Truncate(time.Second)
	if failedAt.Before(now) {
		failedAt = failedAt.Add(time.Second)
	}
	if err := c.Store.RecordPIN(ctx, profileID, failures, failedAt, lockedUntil); err != nil {
		return err
	}
	if !lockedUntil.IsZero() {
		return c.locked(lockedUntil, now)
	}
	return ErrIncorrect
}

func (c *Checker) maxFailures() int {
	if c.MaxFailures > 0 {
		return c.MaxFailures
	}
	return DefaultMaxFailures
}

func (c *Checker) locked(until, now time.Time) error {
	if until.Equal(forever) {
		return &WaitError{Locked: true}
	}
	return &WaitError{Locked: true, Wait: until.Sub(now)}
}
