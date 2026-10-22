package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Totals per title, never per person: a series adds up its episodes;
// viewers are accounts; favorites, profiles.
func TestWatchStats(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	acct1, prof1, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	_, prof2, _ := s.CreateAccount(ctx, "subject-2", owner("2"))
	kid, err := s.AddProfile(ctx, acct1.ID, NewProfile{Name: "Kid", Kind: "standard", JellyfinUserID: "u9", JellyfinUsername: "of-9", JellyfinToken: "t9"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "S", SortName: "s", TMDB: "1399", TVDB: "121361", IMDb: "tt0944947"},
		{ID: id(1), Kind: "movie", Name: "M", SortName: "m", TMDB: "949", IMDb: "tt0113277"}}, 1)
	s.RefreshTitleKeys(ctx)
	day := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	s.RecordPlay(ctx, prof1.ID, "series/tmdb/1399/s1/e1", "", 0, true, day)
	s.RecordPlay(ctx, kid.ID, "series/tmdb/1399/s1/e2", "", 0, true, day.Add(time.Hour)) // the same account
	s.RecordPlay(ctx, prof2.ID, "series/tmdb/1399/s1/e1", "", 0, true, day.Add(-time.Hour))
	s.RecordPlay(ctx, prof2.ID, "movie/tmdb/949", "", 300_000, false, day) // started, not finished
	s.SetFavorite(ctx, prof1.ID, "series/tmdb/1399", "", true, day)
	s.SetFavorite(ctx, prof2.ID, "movie/tmdb/100", "", true, day) // no library has it

	all, err := s.AllWatchStats(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]WatchStats{}
	for _, w := range all {
		got[w.Kind+"/"+w.TMDB] = w
	}
	if w := got["series/1399"]; w.Plays != 3 || w.Viewers != 2 || w.Favorites != 1 || w.TVDB != "121361" || w.IMDb != "tt0944947" ||
		!w.LastPlayed.Equal(day.Add(time.Hour)) {
		t.Errorf("series: %+v", w)
	}
	if w := got["movie/949"]; len(got) != 2 || w.Plays != 0 || w.Viewers != 0 {
		t.Errorf("a film only started counts nothing: %+v (%d titles)", w, len(got))
	}
	if w, ok := got["movie/100"]; ok && w.Favorites != 1 {
		t.Errorf("favorite outside the library: %+v", w)
	}
	if later, _ := s.AllWatchStats(ctx, time.Now().Add(time.Hour)); len(later) != 0 {
		t.Errorf("nothing changed since: %+v", later)
	}

	k, err := s.CreateAppKey(ctx, "Stewarr", "watch-stats", "secret-key", day)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := s.AppKeyFor(ctx, "secret-key", day); err != nil || found.ID != k.ID {
		t.Errorf("key: %+v %v", found, err)
	}
	if _, err := s.AppKeyFor(ctx, "wrong", day); err != ErrNotFound {
		t.Errorf("wrong key: %v", err)
	}
	if keys, _ := s.AppKeys(ctx); len(keys) != 1 || keys[0].LastUsed.IsZero() {
		t.Errorf("keys: %+v", keys)
	}
	s.DeleteAppKey(ctx, k.ID)
	if _, err := s.AppKeyFor(ctx, "secret-key", day); err != ErrNotFound {
		t.Errorf("revoked key: %v", err)
	}
}
