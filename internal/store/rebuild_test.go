package store

import (
	"context"
	"testing"
	"time"
)

// Rebuilding the profiles table keeps everything pointing at a profile:
// sessions, what it did with titles, its import.
func TestProfilesRebuildKeepsReferences(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct, prof, err := s.CreateAccount(ctx, "subject-1", owner("1"))
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := s.CreateSession(ctx, acct.ID, prof.ID, "phone", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "item", 60_000, false, time.Now())
	s.MarkImported(ctx, prof.ID, time.Now())

	body, err := migrations.ReadFile("migrations/017_profiles_without_jellyfin.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.apply(ctx, "017 again", string(body), 17); err != nil {
		t.Fatal(err)
	}
	// The columns profiles got since, back.
	later, _ := migrations.ReadFile("migrations/031_subtitle_language.sql")
	if err := s.apply(ctx, "031 again", string(later), 31); err != nil {
		t.Fatal(err)
	}

	if got, err := s.Profile(ctx, acct.ID, prof.ID); err != nil || got.JellyfinToken != prof.JellyfinToken || got.Name != prof.Name {
		t.Errorf("profile: %+v %v", got, err)
	}
	var active string
	s.db.QueryRowContext(ctx, `SELECT coalesce(active_profile_id, '') FROM sessions WHERE id = ?`, sess.ID).Scan(&active)
	if active != prof.ID {
		t.Errorf("session's profile: %q", active)
	}
	if states, _ := s.PersonalStates(ctx, prof.ID, []string{"movie/tmdb/949"}); states["movie/tmdb/949"].PositionMs != 60_000 {
		t.Errorf("history: %+v", states)
	}
	if ok, _ := s.Imported(ctx, prof.ID); !ok {
		t.Error("import record lost")
	}
	var fk int
	s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys left off")
	}
	// And they still hold: deleting the account takes its profile's rows.
	if err := s.DeleteAccount(ctx, acct.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRowContext(ctx, `SELECT count(*) FROM profile_titles`).Scan(&n)
	if n != 0 {
		t.Errorf("cascade after the rebuild: %d rows left", n)
	}
}

// Continue Watching finds a title wherever its copy is now: the item it was
// last seen as, else another with the same title, else the item as kept.
func TestInProgressFollowsTheTitle(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	s.PutCatalog(ctx, []CatalogItem{{ID: "0000000000000000000000000000000b", Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"}}, 1)
	s.RefreshTitleKeys(ctx)
	now := time.Now()
	// Watched as an item the library no longer has.
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "0000000000000000000000000000000a", 60_000, false, now)
	// And something the index doesn't hold (an audiobook, say).
	s.RecordPlay(ctx, prof.ID, "item/0000000000000000000000000000000c", "0000000000000000000000000000000c", 60_000, false, now.Add(-time.Hour))
	got, err := s.InProgress(ctx, prof.ID, nil, 0, 10)
	if err != nil || len(got) != 2 || got[0] != "0000000000000000000000000000000b" || got[1] != "0000000000000000000000000000000c" {
		t.Errorf("in progress: %v %v", got, err)
	}
	if got, _ := s.InProgress(ctx, prof.ID, []string{"movie"}, 0, 10); len(got) != 1 {
		t.Errorf("films in progress: %v", got)
	}
}
