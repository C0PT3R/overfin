// Package pin checks profile PINs.
//
// A PIN is short, so it is only as strong as the limits on guessing it. Each
// wrong PIN doubles the wait before the next attempt (1, 2, 4, 8 seconds),
// and the fifth locks the profile (SECURITY.md rule 22). PINs are stored as
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

// MaxFailures is how many wrong PINs in a row lock a profile.
const MaxFailures = 5

// LockFor is how long a lock lasts. Rule 22 wants the owner to log in again
// to unlock; until step-up exists, the lock simply expires.
const LockFor = 15 * time.Minute

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
		return &WaitError{Locked: true, Wait: st.LockedUntil.Sub(now)}
	}
	failures := st.Failures
	if !st.LockedUntil.IsZero() || now.Sub(st.FailedAt) > Forget {
		failures = 0 // a lock that ran out, or old mistakes
	}
	if failures > 0 {
		if next := st.FailedAt.Add(time.Second << (failures - 1)); now.Before(next) {
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
	if failures >= MaxFailures {
		lockedUntil = now.Add(LockFor)
	}
	if err := c.Store.RecordPIN(ctx, profileID, failures, now, lockedUntil); err != nil {
		return err
	}
	if !lockedUntil.IsZero() {
		return &WaitError{Locked: true, Wait: LockFor}
	}
	return ErrIncorrect
}
