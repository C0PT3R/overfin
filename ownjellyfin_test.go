package overfin

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/overfin/server/internal/admin"
	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/keys"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/takeover"
	"github.com/overfin/server/internal/takeover/takeovertest"
)

func emptyStore(t *testing.T) *store.Store {
	k, _ := keys.Derive([]byte(strings.Repeat("a", 32)))
	box, _ := seal.New(k.TokenEncryption)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "overfin.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOwnJellyfin(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := func(url string) *config.Config {
		c := &config.Config{}
		c.Jellyfin.URL = url
		return c
	}

	// A fresh Jellyfin is set up at once, with Overfin's own key, saved.
	fresh, url := takeovertest.New(t, false)
	db := emptyStore(t)
	gotURL, key, ok, err := ownJellyfin(ctx, cfg(url), db, logger)
	if err != nil || !ok || gotURL != url || fresh.Keys[key] != takeover.AppName {
		t.Fatalf("fresh: %q %q %v %v", gotURL, key, ok, err)
	}
	if savedURL, savedKey, _, _ := db.Jellyfin(ctx); savedURL != url || savedKey != key {
		t.Errorf("saved %q %q", savedURL, savedKey)
	}
	// Next time it's Overfin's own: nothing more is changed there.
	writes := len(fresh.Writes)
	if _, again, ok, err := ownJellyfin(ctx, cfg(url), db, logger); err != nil || !ok || again != key || len(fresh.Writes) != writes {
		t.Errorf("again: %q %v %v, writes %v", again, ok, err, fresh.Writes)
	}
	// Out of reach, with a key: Overfin starts with what it saved.
	if _, again, ok, err := ownJellyfin(ctx, cfg("http://127.0.0.1:1"), db, logger); err != nil || !ok || again != key {
		t.Errorf("unreachable: %q %v %v", again, ok, err)
	}

	// Someone else's waits to be handed over, untouched.
	other, otherURL := takeovertest.New(t, true)
	if _, _, ok, err := ownJellyfin(ctx, cfg(otherURL), emptyStore(t), logger); err != nil || ok {
		t.Errorf("someone else's: %v %v", ok, err)
	}
	if len(other.Writes) != 0 {
		t.Errorf("changed: %v", other.Writes)
	}
}

func TestTakeOverOnSetupPage(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Fresh, at the address asked.
	_, url := takeovertest.New(t, false)
	db := emptyStore(t)
	got, _, ans, err := takeOver(ctx, db, "", admin.JellyfinRequest{URL: " " + url + "/ "}, logger)
	if err != nil || !ans.Done || got != url || !strings.Contains(ans.Summary, "set up") {
		t.Fatalf("fresh: %q %+v %v", got, ans, err)
	}

	// Someone else's, at the configuration file's address: a login, what
	// it holds, then handed over once confirmed.
	other, otherURL := takeovertest.New(t, true)
	other.Users["alice"] = takeovertest.User{Password: "secret", Admin: true}
	other.Keys["seerr-key"] = "Seerr"
	db = emptyStore(t)
	steps := []struct {
		req  admin.JellyfinRequest
		want func(admin.JellyfinAnswer) bool
	}{
		{admin.JellyfinRequest{URL: "http://ignored"}, func(a admin.JellyfinAnswer) bool { return a.NeedsLogin }},
		{admin.JellyfinRequest{Username: "alice", Password: "secret"}, func(a admin.JellyfinAnswer) bool {
			return a.Survey != nil && slices.Contains(a.Survey.Users, "alice") && a.Survey.Libraries == 1
		}},
		{admin.JellyfinRequest{Username: "alice", Password: "secret", Confirm: "another"}, func(a admin.JellyfinAnswer) bool { return a.Survey != nil }},
	}
	for _, st := range steps {
		if _, _, ans, err := takeOver(ctx, db, otherURL, st.req, logger); err != nil || ans.Done || !st.want(ans) {
			t.Errorf("%+v: %+v %v", st.req, ans, err)
		}
	}
	if _, _, ans, err := takeOver(ctx, db, otherURL, admin.JellyfinRequest{Username: "alice", Password: "nope", Confirm: "server-1"}, logger); err == nil || ans.Done {
		t.Errorf("wrong password: %+v %v", ans, err)
	}
	if len(other.Writes) != 0 {
		t.Fatalf("changed before it was handed over: %v", other.Writes)
	}
	// Closing access fails once: it shows as open, and the next try, with
	// Overfin's key, closes it.
	other.FailPasswords = 1
	if _, _, ans, err := takeOver(ctx, db, otherURL, admin.JellyfinRequest{Username: "alice", Password: "secret", Confirm: "server-1"}, logger); err == nil || ans.Done {
		t.Fatalf("closing failed: %+v %v", ans, err)
	}
	if open, _, _ := db.JellyfinAccess(ctx); !open {
		t.Error("closing failed, recorded as closed")
	}
	got, key, ans, err := takeOver(ctx, db, otherURL, admin.JellyfinRequest{}, logger)
	if err != nil || !ans.Done || got != otherURL || other.Keys[key] != takeover.AppName {
		t.Fatalf("handed over: %q %+v %v", got, ans, err)
	}
	// Overfin's key, then access closed: alice's password nobody knows.
	// Nothing else changed.
	if !slices.Equal(other.Writes, []string{"POST /Auth/Keys", "POST /Users/Password alice"}) || other.Keys["seerr-key"] != "Seerr" || len(other.Tokens) != 0 {
		t.Errorf("changed %v, keys %v, still signed in %d", other.Writes, other.Keys, len(other.Tokens))
	}
	if other.Users["alice"].Password == "secret" {
		t.Error("access is still open")
	}
	if open, _, _ := db.JellyfinAccess(ctx); open {
		t.Error("recorded as open")
	}
}

// Handed over in the terminal (a lost secret key): a login Jellyfin
// refuses comes with how to get one back, since nobody knows its
// administrators' passwords once Overfin took it.
func TestHandOverInTerminalSaysHowToReset(t *testing.T) {
	other, url := takeovertest.New(t, true)
	other.Users["alice"] = takeovertest.User{Password: "secret", Admin: true}
	var out strings.Builder
	_, _, err := handOverInTerminal(context.Background(), emptyStore(t), url, strings.NewReader("overfin-admin\nwrong\n"), &out)
	if err == nil || !strings.Contains(err.Error(), url+"/Users/ForgotPassword") || !strings.Contains(err.Error(), `"EnteredUsername":"overfin-admin"`) {
		t.Errorf("error: %v", err)
	}
}
