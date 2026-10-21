package pin

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-server/internal/seal"
	"github.com/C0PT3R/overfin-server/internal/store"
)

func setup(t *testing.T) (*Checker, *time.Time, string, string) {
	t.Helper()
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, p, err := s.CreateAccount(ctx, "subject", store.NewOwner{Name: "Me", JellyfinUserID: "jf", JellyfinUsername: "of", JellyfinToken: "t"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := &Checker{Key: bytes.Repeat([]byte{7}, 32), Store: s, UnlockAfter: 15 * time.Minute, now: func() time.Time { return now }}
	if err := c.Check(ctx, a.ID, p.ID, ""); err != nil {
		t.Fatalf("no PIN set, but: %v", err)
	}
	if err := s.SetPIN(ctx, a.ID, p.ID, c.Hash(p.ID, "1234")); err != nil {
		t.Fatal(err)
	}
	return c, &now, a.ID, p.ID
}

func TestValid(t *testing.T) {
	for s, want := range map[string]bool{"1234": true, "12345678": true, "123": false, "123456789": false, "12a4": false, "": false, "١٢٣٤": false} {
		if Valid(s) != want {
			t.Errorf("Valid(%q) = %v", s, !want)
		}
	}
}

func TestRightAndWrong(t *testing.T) {
	c, _, acct, prof := setup(t)
	ctx := context.Background()
	if err := c.Check(ctx, acct, prof, ""); !errors.Is(err, ErrRequired) {
		t.Errorf("no PIN given: %v", err)
	}
	if err := c.Check(ctx, acct, prof, "1234"); err != nil {
		t.Errorf("right PIN: %v", err)
	}
	if err := c.Check(ctx, acct, prof, "4321"); !errors.Is(err, ErrIncorrect) {
		t.Errorf("wrong PIN: %v", err)
	}
	// The hash is bound to the profile.
	if bytes.Equal(c.Hash(prof, "1234"), c.Hash("prof_other", "1234")) {
		t.Error("same hash for two profiles")
	}
}

func TestGrowingWaitThenLock(t *testing.T) {
	c, now, acct, prof := setup(t)
	ctx := context.Background()
	var we *WaitError
	for i, wait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if err := c.Check(ctx, acct, prof, "0000"); !errors.Is(err, ErrIncorrect) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		// Too soon: refused without being checked, even with the right PIN.
		if err := c.Check(ctx, acct, prof, "1234"); !errors.As(err, &we) || we.Locked || we.Wait != wait {
			t.Fatalf("after %d failures: %v", i+1, err)
		}
		*now = now.Add(wait)
	}
	if err := c.Check(ctx, acct, prof, "0000"); !errors.As(err, &we) || !we.Locked {
		t.Fatalf("fifth wrong PIN: %v", err)
	}
	*now = now.Add(c.UnlockAfter - time.Second)
	if err := c.Check(ctx, acct, prof, "1234"); !errors.As(err, &we) || !we.Locked {
		t.Fatalf("right PIN while locked: %v", err)
	}
	*now = now.Add(time.Second)
	if err := c.Check(ctx, acct, prof, "1234"); err != nil {
		t.Fatalf("right PIN after the lock: %v", err)
	}
	// The right PIN starts the count over.
	if err := c.Check(ctx, acct, prof, "0000"); !errors.Is(err, ErrIncorrect) {
		t.Fatal(err)
	}
	if err := c.Check(ctx, acct, prof, "1234"); !errors.As(err, &we) || we.Wait != time.Second {
		t.Fatalf("count not reset: %v", err)
	}
}

func TestLockUntilOwnerLogsIn(t *testing.T) {
	c, now, acct, prof := setup(t)
	c.UnlockAfter = 0
	ctx := context.Background()
	for i := 0; i < DefaultMaxFailures; i++ {
		c.Check(ctx, acct, prof, "0000")
		*now = now.Add(time.Minute)
	}
	*now = now.Add(24 * 365 * time.Hour)
	var we *WaitError
	if err := c.Check(ctx, acct, prof, "1234"); !errors.As(err, &we) || !we.Locked || we.Wait != 0 {
		t.Fatalf("a year later: %v", err)
	}
	if err := c.Store.UnlockPINs(ctx, acct); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(ctx, acct, prof, "1234"); err != nil {
		t.Errorf("after the owner's login: %v", err)
	}
}

func TestOperatorThreshold(t *testing.T) {
	c, now, acct, prof := setup(t)
	c.MaxFailures = 3
	ctx := context.Background()
	var we *WaitError
	for i := 0; i < 2; i++ {
		if err := c.Check(ctx, acct, prof, "0000"); !errors.Is(err, ErrIncorrect) {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		*now = now.Add(time.Minute)
	}
	if err := c.Check(ctx, acct, prof, "0000"); !errors.As(err, &we) || !we.Locked {
		t.Fatalf("third wrong PIN: %v", err)
	}
}
