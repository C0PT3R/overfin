package overfin

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/config"
	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/password"
	"github.com/overfin/server/internal/seal"
	"github.com/overfin/server/internal/store"
)

type fakeDirectory map[string]connector.Person

func (d fakeDirectory) People(context.Context) (map[string]connector.Person, error) { return d, nil }

func TestImportPeopleAndSetPassword(t *testing.T) {
	ctx := context.Background()
	box, _ := seal.New(bytes.Repeat([]byte{7}, 32))
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Ann's last login put her in the admins' group.
	db.CreateLogin(ctx, "sub-ann", []string{"zecopter-admins"}, "", time.Hour)
	db.CreateLogin(ctx, "sub-bob", nil, "", time.Hour)
	db.ImportUser(ctx, "sub-cy", "cy@example.org", false) // already there
	dir := fakeDirectory{
		"sub-ann": {Email: "ann@example.org", Active: true},
		"sub-bob": {Email: "bob@example.org", Active: true},
		"sub-cy":  {Email: "cy@example.org", Active: true},
		"sub-dan": {Email: "", Active: true},
		"sub-eve": {Email: "eve@example.org", Active: false},
	}
	var out strings.Builder
	if err := importFrom(ctx, dir, "zecopter-admins", db, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "2 people imported (1 admins); 3 left out") {
		t.Errorf("report: %s", out.String())
	}
	ann, err := db.UserByEmail(ctx, "ann@example.org")
	if err != nil || ann.Subject != "sub-ann" || !ann.Admin {
		t.Errorf("Ann: %+v %v", ann, err)
	}
	if bob, _ := db.UserByEmail(ctx, "bob@example.org"); bob == nil || bob.Subject != "sub-bob" || bob.Admin {
		t.Errorf("Bob: %+v", bob)
	}
	if _, err := db.UserByEmail(ctx, "eve@example.org"); err != store.ErrNotFound {
		t.Errorf("a disabled person was imported: %v", err)
	}

	// A locked-out admin sets a new password from the terminal.
	cfg := &config.Config{Identity: config.Identity{Mode: "managed"}}
	out.Reset()
	if err := setPassword(ctx, cfg, db, "ann@example.org", strings.NewReader("too short\n"), &out); err == nil {
		t.Error("a password against the rules")
	}
	if err := setPassword(ctx, cfg, db, "ann@example.org", strings.NewReader("a new one, 2026!\n"), &out); err != nil {
		t.Errorf("new password: %v %s", err, out.String())
	}
	if ann, _ := db.UserByEmail(ctx, "ann@example.org"); !password.Check(ann.PasswordHash, "a new one, 2026!") {
		t.Error("Ann's password wasn't set")
	}
	if err := setPassword(ctx, cfg, db, "nobody@example.org", strings.NewReader("a new one, 2026!\n"), &out); err == nil {
		t.Error("a password for nobody")
	}
}
