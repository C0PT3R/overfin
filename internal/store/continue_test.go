package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// Continue Watching puts what's in the middle and what's next together,
// the last watched first, and each series once: an episode in the middle
// stands for its series rather than its next one.
func TestContinueWatching(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	ep := func(i int, series string, e int) CatalogItem {
		return CatalogItem{ID: id(i), Kind: "episode", Name: "E", SortName: "e", SeriesID: series, SeasonNumber: n(1), EpisodeNumber: n(e)}
	}
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "A", SortName: "a", TMDB: "1"},
		ep(1, id(0), 1), ep(2, id(0), 2), ep(3, id(0), 3),
		{ID: id(4), Kind: "series", Name: "B", SortName: "b", TMDB: "2"},
		ep(5, id(4), 1), ep(6, id(4), 2),
		{ID: id(7), Kind: "movie", Name: "M", SortName: "m", TMDB: "3"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	key := func(itemID string) string {
		var k string
		if err := s.db.QueryRowContext(ctx, `SELECT title_key FROM catalog_items WHERE id = ?`, itemID).Scan(&k); err != nil {
			t.Fatal(err)
		}
		return k
	}
	now := time.Now()
	s.RecordPlay(ctx, prof.ID, key(id(1)), id(1), 0, true, now.Add(-3*time.Hour))       // A: E1 watched
	s.RecordPlay(ctx, prof.ID, key(id(2)), id(2), 60_000, false, now.Add(-time.Hour))   // A: E2 in the middle
	s.RecordPlay(ctx, prof.ID, key(id(5)), id(5), 0, true, now.Add(-2*time.Hour))       // B: E1 watched, E2 next
	s.RecordPlay(ctx, prof.ID, key(id(7)), id(7), 60_000, false, now.Add(-time.Minute)) // M in the middle

	got, err := s.ContinueWatching(ctx, prof.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{id(7), id(2), id(6)}; !slices.Equal(got, want) {
		t.Errorf("continue = %v, want %v", got, want)
	}
	if got, _ := s.ContinueWatching(ctx, prof.ID, 1, 1); !slices.Equal(got, []string{id(2)}) {
		t.Errorf("second page = %v", got)
	}
}
