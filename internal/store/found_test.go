package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A series known only by its IMDb ID, found on TMDB: the index gets its
// TMDB ID, keeps it when the library is read again, and what a profile
// did with it moves to its new key.
func TestFound(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one := 1
	series := CatalogItem{ID: id(0), Kind: "series", Name: "Monte Cristo", SortName: "m", IMDb: "tt29547746"}
	items := []CatalogItem{series,
		{ID: id(1), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(0), SeasonNumber: &one, EpisodeNumber: &one}}
	s.PutCatalog(ctx, items, 1)
	s.RefreshTitleKeys(ctx)
	s.SetPlayed(ctx, prof.ID, map[string]string{"series/imdb/tt29547746/s1/e1": id(1)}, true, time.Now())

	todo, err := s.TitlesToFind(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(todo) != 1 || todo[0] != (FindTarget{"series", "imdb", "tt29547746"}) {
		t.Fatalf("to find: %v %v", todo, err)
	}
	if err := s.SetFound(ctx, todo[0], "229617", time.Now()); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.TitleKeys(ctx, []string{id(0), id(1)})
	if keys[id(0)] != "series/tmdb/229617" || keys[id(1)] != "series/tmdb/229617/s1/e1" {
		t.Errorf("keys: %v", keys)
	}
	if st, _ := s.PersonalStates(ctx, prof.ID, []string{"series/tmdb/229617/s1/e1"}); !st["series/tmdb/229617/s1/e1"].Played {
		t.Errorf("the mark didn't move: %v", st)
	}
	// Read again from the library (still without a TMDB ID): kept.
	s.PutCatalog(ctx, items, 2)
	s.RefreshTitleKeys(ctx)
	if keys, _ := s.TitleKeys(ctx, []string{id(0)}); keys[id(0)] != "series/tmdb/229617" {
		t.Errorf("after the next sync: %v", keys)
	}
	if todo, _ := s.TitlesToFind(ctx, time.Now().Add(-time.Hour), 10); len(todo) != 0 {
		t.Errorf("still to find: %v", todo)
	}
}
