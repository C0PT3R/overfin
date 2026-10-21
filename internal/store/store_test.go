package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/C0PT3R/overfin-server/internal/ids"
	"github.com/C0PT3R/overfin-server/internal/seal"
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
	path := filepath.Join(t.TempDir(), "demo.db")
	m := fstest.MapFS{
		"001_first.sql":  {Data: []byte(`CREATE TABLE demo_things (id TEXT PRIMARY KEY) STRICT;`)},
		"002_second.sql": {Data: []byte(`ALTER TABLE demo_things ADD COLUMN note TEXT;`)},
	}
	for i := 0; i < 2; i++ { // twice: the second run applies nothing
		db, err := OpenModule(ctx, "demo", path, m)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		db.Close()
	}
	db, _ := OpenModule(ctx, "demo", path, m)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO demo_things (id, note) VALUES ('a', 'b')`); err != nil {
		t.Errorf("table not migrated: %v", err)
	}
	bad := fstest.MapFS{"002_skip.sql": {Data: []byte(`SELECT 1;`)}}
	if _, err := OpenModule(ctx, "other", filepath.Join(t.TempDir(), "other.db"), bad); err == nil {
		t.Error("an out-of-sequence migration was accepted")
	}
}

// A module's own database holds its tables only: the core's aren't there.
func TestModuleDatabaseIsItsOwn(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	s.CreateAccount(ctx, "subject", owner("1"))
	db, err := OpenModule(ctx, "demo", filepath.Join(t.TempDir(), "demo.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"accounts", "profiles", "sessions"} {
		if _, err := db.Exec(`SELECT count(*) FROM ` + table); err == nil {
			t.Errorf("a module's database reaches the core's %s", table)
		}
	}
}

// Installs from before modules had their own databases kept modules' tables
// in the core's: they move, once.
func TestMoveModuleTables(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	m := fstest.MapFS{"001_lists.sql": {Data: []byte(`
		CREATE TABLE demo (id TEXT PRIMARY KEY, name TEXT NOT NULL) STRICT;
		CREATE TABLE demo_items (list TEXT NOT NULL REFERENCES demo(id) ON DELETE CASCADE, item TEXT NOT NULL) STRICT;`)}}
	// The old way: the module's tables in the core's database.
	if _, err := s.db.Exec(string(m["001_lists.sql"].Data) + `
		INSERT INTO demo VALUES ('l1', 'Films');
		INSERT INTO demo_items VALUES ('l1', 'item_a'), ('l1', 'item_b');
		INSERT INTO module_migrations (module, version) VALUES ('demo', 1);
		CREATE TABLE demonstration (x TEXT);`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "demo.db")
	db, err := OpenModule(ctx, "demo", path, m)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	moved, err := s.MoveModuleTables(ctx, "demo", path)
	if err != nil || moved != 2 {
		t.Fatalf("moved %d tables: %v", moved, err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM demo_items`).Scan(&n)
	if n != 2 {
		t.Errorf("rows in the module's database: %d", n)
	}
	if _, err := s.db.Exec(`SELECT count(*) FROM demo_items`); err == nil {
		t.Error("the old table is still in the core's database")
	}
	// Another module's table that only starts with the same letters stays.
	if _, err := s.db.Exec(`SELECT count(*) FROM demonstration`); err != nil {
		t.Errorf("a table not the module's was moved: %v", err)
	}
	// Once only.
	if moved, err := s.MoveModuleTables(ctx, "demo", path); err != nil || moved != 0 {
		t.Errorf("second move: %d %v", moved, err)
	}
}

type recorder struct{ profiles, accounts []string }

func (r *recorder) ForgetProfiles(_ context.Context, ids []string) {
	r.profiles = append(r.profiles, ids...)
}
func (r *recorder) ForgetAccount(_ context.Context, id string) { r.accounts = append(r.accounts, id) }

// Deleting profiles and accounts tells the forgetter, which tells modules.
func TestDeletionsAreForgotten(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	r := &recorder{}
	s.SetForgetter(r)
	a, own, _ := s.CreateAccount(ctx, "subject", owner("1"))
	kid, err := s.AddProfile(ctx, a.ID, newProfile("Kid", "k"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProfile(ctx, a.ID, kid.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.profiles) != 1 || r.profiles[0] != kid.ID {
		t.Errorf("profile deletion: %v", r.profiles)
	}
	if err := s.DeleteAccount(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(r.profiles) != 2 || r.profiles[1] != own.ID || len(r.accounts) != 1 || r.accounts[0] != a.ID {
		t.Errorf("account deletion: %v %v", r.profiles, r.accounts)
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

func TestJellyfinSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	box, _ := seal.New(bytes.Repeat([]byte{9}, 32))
	s, err := Open(ctx, path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, ok, err := s.Jellyfin(ctx); ok || err != nil {
		t.Fatalf("before setup: ok %v, err %v", ok, err)
	}
	if err := s.SetJellyfin(ctx, "http://192.0.2.10:8096", "admin-key", "server-1"); err != nil {
		t.Fatal(err)
	}
	if id, err := s.JellyfinServer(ctx); id != "server-1" || err != nil {
		t.Errorf("server ID: %q %v", id, err)
	}
	url, key, ok, err := s.Jellyfin(ctx)
	if !ok || err != nil || url != "http://192.0.2.10:8096" || key != "admin-key" {
		t.Errorf("after setup: %q %q %v %v", url, key, ok, err)
	}
	// The key is sealed: never stored as is.
	var raw []byte
	s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'jellyfin-key'`).Scan(&raw)
	if bytes.Contains(raw, []byte("admin-key")) {
		t.Error("Jellyfin's key is stored in clear")
	}
	// Read-only, for -check; another secret key can't read it.
	if _, key, ok, err := ReadJellyfin(ctx, path, box); !ok || err != nil || key != "admin-key" {
		t.Errorf("read-only: %q %v %v", key, ok, err)
	}
	other, _ := seal.New(bytes.Repeat([]byte{8}, 32))
	if _, _, _, err := ReadJellyfin(ctx, path, other); err == nil {
		t.Error("another secret key read Jellyfin's key")
	}
}

func TestCatalogTitles(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := old.AddDate(0, 6, 0)
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "1", Name: "Old", Overview: ""}}, old); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "2", Name: "Heat", Overview: "", Poster: "/abc12345.jpg"},
		{Kind: "movie", TMDB: "3", Name: "Arrival", Overview: ""}}, now); err != nil {
		t.Fatal(err)
	}
	// The library has film 2.
	s.PutCatalog(ctx, []CatalogItem{{ID: "0123456789abcdef0123456789abcdef", Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "2"}}, 1)
	if err := s.SetList(ctx, "trending", "movie", []string{"3", "2", "1"}); err != nil {
		t.Fatal(err)
	}
	names := func(avail string) string {
		got, err := s.ListTitles(ctx, "trending", "movie", avail, 0, 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, ti := range got {
			out = append(out, ti.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("all"); got != "Arrival,Heat,Old" {
		t.Errorf("all: %s", got)
	}
	if got := names("library"); got != "Heat" {
		t.Errorf("library: %s", got)
	}
	if got := names("missing"); got != "Arrival,Old" {
		t.Errorf("missing: %s", got)
	}
	if ti, _ := s.TitleByTMDB(ctx, "movie", "2"); ti == nil || ti.ItemID == "" || ti.Poster != "/abc12345.jpg" {
		t.Errorf("title: %+v", ti)
	}
	// TMDB's data is kept six months at most: the old title goes, from
	// the list too.
	if n, err := s.PurgeTitles(ctx, now.Add(-150*24*time.Hour)); err != nil || n != 1 {
		t.Errorf("purged %d, %v", n, err)
	}
	if got := names("all"); got != "Arrival,Heat" {
		t.Errorf("after the purge: %s", got)
	}
	// Disconnected from Seerr: all of it goes.
	if err := s.ForgetTMDB(ctx); err != nil || names("all") != "" {
		t.Errorf("forgotten: %v %s", err, names("all"))
	}
}

func TestPersonal(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, p, err := s.CreateAccount(ctx, "subject", owner("p"))
	if err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
		{ID: id(2), Kind: "movie", Name: "Heat (4K)", SortName: "Heat", TMDB: "949"}, // another copy
		{ID: id(3), Kind: "series", Name: "Dark", SortName: "Dark", TVDB: "334824"},
		{ID: id(4), Kind: "episode", Name: "Secrets", SortName: "Secrets", SeriesID: id(3), SeasonNumber: &one, EpisodeNumber: &two},
		{ID: id(5), Kind: "movie", Name: "Home video", SortName: "Home video"},
	}, 1)
	keys, err := s.TitleKeys(ctx, []string{id(1), id(2), id(4), id(5), id(9)})
	if err != nil {
		t.Fatal(err)
	}
	for item, want := range map[string]string{id(1): "movie/tmdb/949", id(2): "movie/tmdb/949",
		id(4): "series/tvdb/334824/s1/e2", id(5): "item/" + id(5), id(9): "item/" + id(9)} {
		if keys[item] != want {
			t.Errorf("%s: %q, want %q", item, keys[item], want)
		}
	}

	// Two copies of one film add up.
	at := time.Unix(1_800_000_000, 0)
	if err := s.MergePersonal(ctx, p.ID, []Personal{
		{Title: "movie/tmdb/949", ItemID: id(1), Played: true, PlayCount: 2, LastPlayedAt: at},
		{Title: "movie/tmdb/949", ItemID: id(2), PositionMs: 60_000, Favorite: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.PersonalStates(ctx, p.ID, []string{"movie/tmdb/949", "series/tmdb/1"})
	if err != nil {
		t.Fatal(err)
	}
	h := got["movie/tmdb/949"]
	if len(got) != 1 || !h.Played || h.PlayCount != 2 || h.PositionMs != 60_000 || !h.Favorite || !h.LastPlayedAt.Equal(at) {
		t.Errorf("merged: %+v", got)
	}

	// Imports, once per profile.
	if todo, _ := s.ProfilesToImport(ctx); len(todo) != 1 {
		t.Fatalf("to import: %d", len(todo))
	}
	s.MarkImported(ctx, p.ID, at)
	if todo, _ := s.ProfilesToImport(ctx); len(todo) != 0 {
		t.Errorf("imported twice: %d", len(todo))
	}
}
