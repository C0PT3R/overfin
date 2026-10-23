package takeover_test

import (
	"context"
	"errors"
	"testing"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/takeover"
	"github.com/overfin/server/internal/takeover/takeovertest"
)

func newFake(t *testing.T, setUp bool) (*takeovertest.Jellyfin, *jellyfin.Client) {
	f, url := takeovertest.New(t, setUp)
	jf, err := jellyfin.New(url, "test")
	if err != nil {
		t.Fatal(err)
	}
	return f, jf
}

func TestFreshServerIsSetUp(t *testing.T) {
	ctx := context.Background()
	f, jf := newFake(t, false)
	found, err := takeover.Look(ctx, jf, "", "")
	if err != nil || found.State != takeover.Fresh || found.Server.ID != "server-1" {
		t.Fatalf("Look = %+v, %v; want fresh", found, err)
	}
	key, err := takeover.SetUp(ctx, jf, jellyfin.Startup{UICulture: "en-US", Country: "US", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Keys[key] != takeover.AppName {
		t.Errorf("key %q isn't Overfin's", key)
	}
	if _, ok := f.Users[takeover.AdminName]; !ok || !f.SetUp {
		t.Errorf("not set up: %+v", f)
	}
	if len(f.Tokens) != 0 || f.LoggedOut != 1 {
		t.Errorf("still signed in: %d tokens, %d sign-outs", len(f.Tokens), f.LoggedOut)
	}
	// From then on, it's Overfin's.
	if found, err := takeover.Look(ctx, jf, key, "server-1"); err != nil || found.State != takeover.Ours {
		t.Errorf("after setup: %+v, %v", found, err)
	}
}

func TestOtherServerIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	f, jf := newFake(t, true)
	f.Users["alice"] = takeovertest.User{Password: "secret", Admin: true}
	f.Users["bob"] = takeovertest.User{Password: "pw"}
	f.Keys["seerr-key"] = "Seerr"

	for _, c := range []struct{ key, server string }{
		{"", ""},                  // no key
		{"lost", "server-1"},      // a key it doesn't take
		{"seerr-key", "server-2"}, // a key for another server
	} {
		if found, err := takeover.Look(ctx, jf, c.key, c.server); err != nil || found.State != takeover.Other {
			t.Errorf("takeover.Look(%q, %q) = %+v, %v; want other", c.key, c.server, found, err)
		}
	}
	// Its first-run setup is never run again.
	if _, err := takeover.SetUp(ctx, jf, jellyfin.Startup{}); err == nil {
		t.Error("set up a server that was set up already")
	}
	if len(f.Writes) != 0 {
		t.Errorf("changed: %v", f.Writes)
	}

	// Inspecting it changes nothing.
	s, err := takeover.Inspect(ctx, jf, "alice", "secret")
	if err != nil || len(s.Users) != 2 || s.Libraries != 1 {
		t.Errorf("Inspect = %+v, %v", s, err)
	}
	if !errors.Is(func() error { _, err := takeover.Inspect(ctx, jf, "alice", "wrong"); return err }(), takeover.ErrLogin) {
		t.Error("a wrong password isn't takeover.ErrLogin")
	}
	if !errors.Is(func() error { _, err := takeover.HandOver(ctx, jf, "bob", "pw"); return err }(), takeover.ErrNotAdmin) {
		t.Error("a user who isn't an admin could hand it over")
	}
	if len(f.Writes) != 0 || len(f.Tokens) != 0 {
		t.Errorf("changed: %v; still signed in: %d", f.Writes, len(f.Tokens))
	}

	// Handed over, it gets Overfin's key, and nothing else changes.
	key, err := takeover.HandOver(ctx, jf, "alice", "secret")
	if err != nil || f.Keys[key] != takeover.AppName {
		t.Fatalf("HandOver = %q, %v", key, err)
	}
	if len(f.Writes) != 1 || f.Keys["seerr-key"] != "Seerr" || len(f.Tokens) != 0 {
		t.Errorf("changed: %v, keys %v, signed in %d", f.Writes, f.Keys, len(f.Tokens))
	}
	if found, err := takeover.Look(ctx, jf, key, "server-1"); err != nil || found.State != takeover.Ours {
		t.Errorf("after the handover: %+v, %v", found, err)
	}
}
