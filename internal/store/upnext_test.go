package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// What playing a series plays: nothing watched, the first episode (specials
// last); one in the middle, it, where it stopped; one finished, the next;
// the last finished, the first again.
func TestSeriesUpNext(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	ep := func(i, season, e int) CatalogItem {
		return CatalogItem{ID: id(i), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(season), EpisodeNumber: n(e)}
	}
	first := ep(2, 1, 1)
	first.Tags = []string{"Pilot"}
	s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "S", SortName: "s", TMDB: "1"},
		ep(1, 0, 1), first, ep(3, 1, 2), ep(4, 2, 1)}, 1)
	s.RefreshTitleKeys(ctx)
	series := TitleRef{Kind: "series", Source: "tmdb", ID: "1"}
	// A profile the first episode is hidden from starts at the next one it
	// may see.
	if key, _, err := s.SeriesUpNext(ctx, prof.ID, series, &Access{BlockedTags: []string{"pilot"}}); key != "series/tmdb/1/s1/e2" || err != nil {
		t.Errorf("first one hidden: %q %v", key, err)
	}
	check := func(about, want string, wantPos int64) {
		t.Helper()
		key, pos, err := s.SeriesUpNext(ctx, prof.ID, series, nil)
		if err != nil || key != want || pos != wantPos {
			t.Errorf("%s: %q %d %v, want %q %d", about, key, pos, err, want, wantPos)
		}
	}
	check("nothing watched", "series/tmdb/1/s1/e1", 0)
	now := time.Now()
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e1", id(2), 0, true, now.Add(-3*time.Hour))
	check("one finished", "series/tmdb/1/s1/e2", 0)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e2", id(3), 600_000, false, now.Add(-2*time.Hour))
	check("one in the middle", "series/tmdb/1/s1/e2", 600_000)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e2", id(3), 0, true, now.Add(-time.Hour))
	check("into the next season", "series/tmdb/1/s2/e1", 0)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s2/e1", id(4), 0, true, now.Add(-time.Minute))
	check("the end: the first again", "series/tmdb/1/s1/e1", 0)
	if key, _, err := s.SeriesUpNext(ctx, prof.ID, TitleRef{Kind: "series", Source: "tmdb", ID: "2"}, nil); key != "" || err != nil {
		t.Errorf("no episodes: %q %v", key, err)
	}
}

// Several episodes finished at once, as when a season is marked played: the
// one after the latest of them. One finished that no library has: the one
// after it all the same.
func TestSeriesUpNextAfterMarking(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	ep := func(i, season, e int) CatalogItem {
		return CatalogItem{ID: id(i), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(season), EpisodeNumber: n(e)}
	}
	s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "S", SortName: "s", TMDB: "1"},
		ep(1, 0, 1), ep(2, 1, 1), ep(3, 1, 2), ep(4, 2, 1), ep(5, 2, 2)}, 1)
	s.RefreshTitleKeys(ctx)
	series := TitleRef{Kind: "series", Source: "tmdb", ID: "1"}
	check := func(about, want string) {
		t.Helper()
		if key, _, err := s.SeriesUpNext(ctx, prof.ID, series, nil); err != nil || key != want {
			t.Errorf("%s: %q %v, want %q", about, key, err, want)
		}
	}
	now := time.Now()
	for _, e := range []string{"s1/e2", "s0/e1", "s1/e1"} {
		s.RecordPlay(ctx, prof.ID, "series/tmdb/1/"+e, "", 0, true, now.Add(-time.Hour))
	}
	check("a season marked", "series/tmdb/1/s2/e1")
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s2/e9", "", 0, true, now)
	check("the end, past the library's", "series/tmdb/1/s1/e1")
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e7", "", 0, true, now.Add(time.Minute))
	check("one no library has", "series/tmdb/1/s2/e1")
}

// A series taken out of Continue Watching stays out until watched again.
func TestDismissContinue(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "S", SortName: "s", TMDB: "1"},
		{ID: id(1), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(1), EpisodeNumber: n(1)},
		{ID: id(2), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(1), EpisodeNumber: n(2)},
		{ID: id(3), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(1), EpisodeNumber: n(3)}}, 1)
	s.RefreshTitleKeys(ctx)
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e1", id(1), 0, true, time.Now().Add(-time.Hour))
	listed := func() int {
		keys, err := s.ContinueTitles(ctx, prof.ID, "2099-01-01", 10)
		if err != nil {
			t.Fatal(err)
		}
		return len(keys)
	}
	if listed() != 1 {
		t.Fatal("next episode not listed")
	}
	s.DismissContinue(ctx, prof.ID, TitleRef{Kind: "series", Source: "tmdb", ID: "1"}, time.Now())
	if listed() != 0 {
		t.Error("dismissed, still listed")
	}
	s.RecordPlay(ctx, prof.ID, "series/tmdb/1/s1/e2", id(2), 0, true, time.Now().Add(time.Minute))
	if listed() != 1 {
		t.Error("watched again, not listed")
	}
}
