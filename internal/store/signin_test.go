package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Users: one per email, whatever its case; disabling or deleting someone
// ends their logins.
func TestUsers(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	p, err := s.CreateUser(ctx, "Alice@Example.org", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice@example.org", false); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("same email: %v", err)
	}
	got, err := s.UserByEmail(ctx, "ALICE@example.org")
	if err != nil || got.Subject != p.Subject || !got.Admin || got.PasswordHash != "" {
		t.Fatalf("by email: %+v, %v", got, err)
	}
	if err := s.SetPassword(ctx, p.Subject, "hash"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UserBySubject(ctx, p.Subject); got.PasswordHash != "hash" {
		t.Errorf("password: %+v", got)
	}
	if ok, _ := s.HasAdmin(ctx); !ok {
		t.Error("no admin")
	}
	_, token, _ := s.CreateLogin(ctx, p.Subject, nil, "", time.Hour)
	if err := s.SetUserDisabled(ctx, p.Subject, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoginByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("login after disabling: %v", err)
	}
	if got, _ := s.UserBySubject(ctx, p.Subject); !got.Disabled {
		t.Error("not disabled")
	}
	if err := s.DeleteUser(ctx, p.Subject); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySubject(ctx, p.Subject); !errors.Is(err, ErrNotFound) {
		t.Errorf("after deleting: %v", err)
	}
}

// Registering, confirming with a link, and the invitation picked up then.
func TestRegistration(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.RegisterUser(ctx, "new@example.org", "hash", "invite-code")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterUser(ctx, "NEW@example.org", "hash", ""); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("twice: %v", err)
	}
	if got, _ := s.UserByEmail(ctx, "new@example.org"); got.Confirmed || got.PasswordHash != "hash" {
		t.Errorf("registered: %+v", got)
	}
	s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("L1"), Purpose: "verify", Expires: time.Now().Add(time.Hour)})
	// A newer link replaces the first.
	s.AddEmailLink(ctx, EmailLink{Subject: u.Subject, LinkHash: []byte("L2"), Purpose: "verify", Expires: time.Now().Add(time.Hour)})
	if _, err := s.TakeEmailLink(ctx, "verify", []byte("L1")); !errors.Is(err, ErrNotFound) {
		t.Errorf("an older link: %v", err)
	}
	if _, err := s.TakeEmailLink(ctx, "reset", []byte("L2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("another purpose: %v", err)
	}
	if sub, err := s.TakeEmailLink(ctx, "verify", []byte("L2")); err != nil || sub != u.Subject {
		t.Errorf("the link: %q %v", sub, err)
	}
	if _, err := s.TakeEmailLink(ctx, "verify", []byte("L2")); !errors.Is(err, ErrNotFound) {
		t.Errorf("used twice: %v", err)
	}
	if code, err := s.ConfirmUser(ctx, u.Subject); err != nil || code != "invite-code" {
		t.Errorf("confirmed: %q %v", code, err)
	}
	if code, _ := s.ConfirmUser(ctx, u.Subject); code != "" {
		t.Error("the invitation is kept after it's given")
	}
	if got, _ := s.UserByEmail(ctx, "new@example.org"); !got.Confirmed {
		t.Error("not confirmed")
	}
	// Those who never confirm go.
	s.RegisterUser(ctx, "late@example.org", "hash", "")
	if n, err := s.DeleteUnconfirmed(ctx, time.Now().Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("unconfirmed: %d %v", n, err)
	}
	s.SetMustChange(ctx, u.Subject, true)
	if got, _ := s.UserByEmail(ctx, "new@example.org"); !got.MustChange {
		t.Error("must change")
	}
	if r, _ := s.PasswordRules(ctx); r.MinLength != 10 || !r.Digit || !r.Special {
		t.Errorf("default rules: %+v", r)
	}
}
