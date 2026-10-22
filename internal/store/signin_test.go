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

// Codes: only from the browser that asked, for their purpose; a match uses
// up every code of the email; five misses end a code; a link is single use.
func TestSigninCodes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	add := func(code, link, binding, purpose string) {
		if err := s.AddSigninCode(ctx, SigninCode{Email: "a@example.org", CodeHash: []byte(code), LinkHash: []byte(link),
			Binding: []byte(binding), Purpose: purpose, Expires: time.Now().Add(10 * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	take := func(code, binding, purpose string) bool {
		ok, err := s.TakeSigninCode(ctx, "A@example.org", purpose, []byte(binding), []byte(code))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	add("111111", "L1", "B1", "signin")
	add("222222", "L2", "B1", "signin")
	if take("111111", "B2", "signin") || take("111111", "B1", "fresh") {
		t.Error("another browser or purpose")
	}
	if !take("222222", "B1", "signin") {
		t.Error("the latest code")
	}
	if take("111111", "B1", "signin") {
		t.Error("used up by the match")
	}
	if n, _ := s.CodesSentSince(ctx, "a@example.org", time.Now().Add(-time.Minute)); n != 0 {
		t.Errorf("sent since: %d", n)
	}

	add("333333", "L3", "B1", "signin")
	for range MaxCodeAttempts {
		take("000000", "B1", "signin")
	}
	if take("333333", "B1", "signin") {
		t.Error("after five misses")
	}

	add("444444", "L4", "B1", "fresh")
	if email, purpose, err := s.TakeSigninLink(ctx, []byte("L4")); err != nil || email != "a@example.org" || purpose != "fresh" {
		t.Errorf("link: %q %q %v", email, purpose, err)
	}
	if _, _, err := s.TakeSigninLink(ctx, []byte("L4")); !errors.Is(err, ErrNotFound) {
		t.Errorf("link twice: %v", err)
	}
}
