package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/C0PT3R/overfin-gateway/internal/ids"
	"github.com/C0PT3R/overfin-gateway/internal/seal"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	box, err := seal.New(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func owner(n string) NewOwner {
	return NewOwner{Name: "Me", JellyfinUserID: "jf-" + n, JellyfinUsername: "of-" + n, JellyfinToken: "token-" + n}
}

func TestAccountAndOwnerProfile(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	acct, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	if !ids.Valid(ids.Account, acct.ID) || !ids.Valid(ids.Profile, prof.ID) {
		t.Errorf("bad IDs: %s, %s", acct.ID, prof.ID)
	}

	got, err := s.AccountBySubject(ctx, "subject-1")
	if err != nil || got.ID != acct.ID {
		t.Fatalf("AccountBySubject = %+v, %v", got, err)
	}

	p, err := s.Profile(ctx, acct.ID, prof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Owner || p.JellyfinToken != "token-1" {
		t.Errorf("profile = %+v", p)
	}

	// The same subject can't get a second account.
	if _, _, err := s.CreateAccount(ctx, "subject-1", owner("2")); err == nil {
		t.Error("a second account was created for the same subject")
	}
}

func TestProfileOfAnotherAccountIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _, _ := s.CreateAccount(ctx, "subject-a", owner("a"))
	_, bProf, _ := s.CreateAccount(ctx, "subject-b", owner("b"))

	if _, err := s.Profile(ctx, a.ID, bProf.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestTokensAreSealedAndBoundToTheirRow(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, aProf, _ := s.CreateAccount(ctx, "subject-a", owner("a"))
	_, bProf, _ := s.CreateAccount(ctx, "subject-b", owner("b"))

	var stored []byte
	if err := s.db.QueryRow(`SELECT jellyfin_token FROM profiles WHERE id = ?`, aProf.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("token-a")) {
		t.Fatal("token stored in plain text")
	}

	// Someone with write access copies b's sealed token into a's row.
	if _, err := s.db.Exec(`UPDATE profiles SET jellyfin_token = (SELECT jellyfin_token FROM profiles WHERE id = ?) WHERE id = ?`,
		bProf.ID, aProf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Profile(ctx, a.ID, aProf.ID); err == nil {
		t.Error("a token moved from another profile was accepted")
	}
}

func TestOneOwnerPerAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _, _ := s.CreateAccount(ctx, "subject-a", owner("a"))

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO profiles (id, account_id, name, kind, owner, jellyfin_user_id,
			jellyfin_username, jellyfin_token, created_at)
		VALUES (?, ?, 'Second', 'standard', 1, 'jf-x', 'of-x', x'00', 0)`,
		ids.New(ids.Profile), a.ID)
	if err == nil {
		t.Error("the database accepted a second owner profile")
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, prof, _ := s.CreateAccount(ctx, "subject-a", owner("a"))

	sess, token, err := s.CreateSession(ctx, a.ID, prof.ID, "Firefox on Linux", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	var stored []byte
	_ = s.db.QueryRow(`SELECT token_hash FROM sessions WHERE id = ?`, sess.ID).Scan(&stored)
	if bytes.Contains(stored, []byte(token)) {
		t.Fatal("session token stored in plain text")
	}

	got, err := s.SessionByToken(ctx, token)
	if err != nil || got.ID != sess.ID || got.ActiveProfileID != prof.ID {
		t.Fatalf("SessionByToken = %+v, %v", got, err)
	}
	if _, err := s.SessionByToken(ctx, token+"x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong token: err = %v", err)
	}

	// Another account can't end this session.
	b, _, _ := s.CreateAccount(ctx, "subject-b", owner("b"))
	if err := s.DeleteSession(ctx, b.ID, sess.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account ended the session: %v", err)
	}

	// Expiry.
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.SessionByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session still found: %v", err)
	}
	if n, _ := s.DeleteExpiredSessions(ctx); n != 1 {
		t.Errorf("deleted %d expired sessions, want 1", n)
	}
}

func TestDeletingAnAccountDeletesEverything(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, prof, _ := s.CreateAccount(ctx, "subject-a", owner("a"))
	_, _, _ = s.CreateSession(ctx, a.ID, prof.ID, "", time.Hour)

	if _, err := s.db.Exec(`DELETE FROM accounts WHERE id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.db.QueryRow(`SELECT (SELECT count(*) FROM profiles) + (SELECT count(*) FROM sessions)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d profiles or sessions survived their account", n)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	path := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path, box)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func newProfile(name, n string) NewProfile {
	return NewProfile{Name: name, Kind: "standard", JellyfinUserID: "jf-" + n, JellyfinUsername: "of-" + n, JellyfinToken: "t-" + n}
}

func TestProfilesInAnAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _, _ := s.CreateAccount(ctx, "subject-a", owner("a"))

	kid, err := s.AddProfile(ctx, a.ID, newProfile("Kids", "k"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if kid.Owner {
		t.Error("an added profile is an owner")
	}
	if _, err := s.AddProfile(ctx, a.ID, newProfile("Kids", "k2"), 3); !errors.Is(err, ErrNameTaken) {
		t.Errorf("same name: %v", err)
	}
	if _, err := s.AddProfile(ctx, a.ID, newProfile("Third", "k3"), 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddProfile(ctx, a.ID, newProfile("Fourth", "k4"), 3); !errors.Is(err, ErrProfileLimit) {
		t.Errorf("over the limit: %v", err)
	}

	// Names are a namespace per account: another account can have "Kids".
	b, _, _ := s.CreateAccount(ctx, "subject-b", owner("b"))
	if _, err := s.AddProfile(ctx, b.ID, newProfile("Kids", "b-k"), 3); err != nil {
		t.Errorf("another account's Kids: %v", err)
	}

	if err := s.RenameProfile(ctx, a.ID, kid.ID, "Me"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("rename to an existing name: %v", err)
	}
	if err := s.RenameProfile(ctx, a.ID, kid.ID, "Little ones"); err != nil {
		t.Error(err)
	}
	if err := s.RenameProfile(ctx, b.ID, kid.ID, "Stolen"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another account renamed the profile: %v", err)
	}
}

func TestActiveProfileStaysInTheAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, aOwner, _ := s.CreateAccount(ctx, "subject-a", owner("a"))
	b, bOwner, _ := s.CreateAccount(ctx, "subject-b", owner("b"))
	sess, _, _ := s.CreateSession(ctx, a.ID, aOwner.ID, "laptop", time.Hour)

	if err := s.SetActiveProfile(ctx, sess.ID, a.ID, bOwner.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("switched to another account's profile: %v", err)
	}
	kid, _ := s.AddProfile(ctx, a.ID, newProfile("Kids", "k"), 0)
	if err := s.SetActiveProfile(ctx, sess.ID, a.ID, kid.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Sessions(ctx, a.ID)
	if len(list) != 1 || list[0].ActiveProfileID != kid.ID {
		t.Errorf("sessions: %+v", list)
	}
	if list, _ := s.Sessions(ctx, b.ID); len(list) != 0 {
		t.Error("another account sees the session")
	}
}

func TestKeyCheckAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	oldBox, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	s, err := Open(ctx, path, oldBox)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckKey(ctx, []byte("old")); err != nil {
		t.Fatalf("first check records it: %v", err)
	}
	_, prof, _ := s.CreateAccount(ctx, "subject", owner("1"))
	s.Close()

	// The key was lost: a new one doesn't pass, and the token can't be read.
	newBox, _ := seal.New(bytes.Repeat([]byte{2}, 32))
	s, err = Open(ctx, path, newBox)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CheckKey(ctx, []byte("new")); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("new key accepted: %v", err)
	}
	broken, err := s.UnreadableTokens(ctx)
	if err != nil || len(broken) != 1 || broken[0].ProfileID != prof.ID || broken[0].JellyfinUsername != "of-1" {
		t.Fatalf("unreadable = %+v, %v", broken, err)
	}

	// Recovery: a fresh token, then the new key is the database's.
	if err := s.SetJellyfinToken(ctx, prof.ID, "fresh"); err != nil {
		t.Fatal(err)
	}
	if broken, _ := s.UnreadableTokens(ctx); len(broken) != 0 {
		t.Errorf("still unreadable: %+v", broken)
	}
	if err := s.SetKeyCheck(ctx, []byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckKey(ctx, []byte("new")); err != nil {
		t.Errorf("after recovery: %v", err)
	}
	if p, err := s.Profile(ctx, prof.AccountID, prof.ID); err != nil || p.JellyfinToken != "fresh" {
		t.Errorf("profile after recovery: %+v %v", p, err)
	}
}

func TestLinkedProfiles(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	_, created, _ := s.CreateAccount(ctx, "a", owner("1")) // "of-1"
	_, adopted, _ := s.CreateAccount(ctx, "b", NewOwner{Name: "Me", JellyfinUserID: "jf-alice", JellyfinUsername: "alice", JellyfinToken: "t"})

	linked, err := s.LinkedProfiles(ctx)
	if err != nil || len(linked) != 1 || linked[0].ProfileID != adopted.ID {
		t.Fatalf("linked = %+v, %v (created %s)", linked, err, created.ID)
	}
	if err := s.SetJellyfinUser(ctx, adopted.ID, "of-moved", "new-token"); err != nil {
		t.Fatal(err)
	}
	if linked, _ := s.LinkedProfiles(ctx); len(linked) != 0 {
		t.Errorf("still linked after the move: %+v", linked)
	}
	if p, _ := s.Profile(ctx, adopted.AccountID, adopted.ID); p.JellyfinUsername != "of-moved" || p.JellyfinToken != "new-token" {
		t.Errorf("profile after the move: %+v", p)
	}
}

func TestModuleMigrations(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	m := fstest.MapFS{
		"001_first.sql":  {Data: []byte(`CREATE TABLE demo_things (id TEXT PRIMARY KEY) STRICT;`)},
		"002_second.sql": {Data: []byte(`ALTER TABLE demo_things ADD COLUMN note TEXT;`)},
	}
	for i := 0; i < 2; i++ { // twice: the second run applies nothing
		if err := s.MigrateModule(ctx, "demo", m); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO demo_things (id, note) VALUES ('a', 'b')`); err != nil {
		t.Errorf("table not migrated: %v", err)
	}
	bad := fstest.MapFS{"002_skip.sql": {Data: []byte(`SELECT 1;`)}}
	if err := s.MigrateModule(ctx, "other", bad); err == nil {
		t.Error("an out-of-sequence migration was accepted")
	}
}

func TestAccountTemplates(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _, _ := s.CreateAccount(ctx, "subject", owner("1"))

	// Every account starts on the minimal Default.
	got, _ := s.AccountByID(ctx, a.ID)
	if got.Template.ID != DefaultTemplate || got.Template.MaxProfiles != 1 || got.Template.MaxStreams != 1 {
		t.Fatalf("new account's template: %+v", got.Template)
	}

	family, err := s.CreateTemplate(ctx, "Family", 3, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, "Family", 1, 1); !errors.Is(err, ErrTemplateNameTaken) {
		t.Errorf("same name twice: %v", err)
	}
	if err := s.SetAccountTemplate(ctx, a.ID, family.ID); err != nil {
		t.Fatal(err)
	}
	// Accounts follow their template live.
	_ = s.UpdateTemplate(ctx, family.ID, "Family", 4, 5)
	if got, _ := s.AccountByID(ctx, a.ID); got.Template.MaxStreams != 4 {
		t.Errorf("after editing the template: %+v", got.Template)
	}
	if err := s.DeleteTemplate(ctx, family.ID); !errors.Is(err, ErrTemplateInUse) {
		t.Errorf("deleting a template in use: %v", err)
	}
	if err := s.DeleteTemplate(ctx, DefaultTemplate); !errors.Is(err, ErrTemplateInUse) {
		t.Errorf("deleting Default: %v", err)
	}
	if err := s.SetAccountTemplate(ctx, a.ID, "tmpl_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown template: %v", err)
	}
	all, _ := s.Templates(ctx)
	if len(all) != 2 || all[0].ID != DefaultTemplate || all[1].Accounts != 1 {
		t.Errorf("templates: %+v %+v", all[0], all[1])
	}
	_ = s.SetAccountTemplate(ctx, a.ID, DefaultTemplate)
	if err := s.DeleteTemplate(ctx, family.ID); err != nil {
		t.Errorf("deleting an unused template: %v", err)
	}
}
