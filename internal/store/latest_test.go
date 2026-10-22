package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A series counts as new when one of its episodes is: "latest" orders it by
// its newest episode, and lists no seasons or episodes of its own.
func TestLatestOrdersSeriesByTheirNewestEpisode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Film", SortName: "a", LibraryID: "lib", AddedAt: day(10)},
		{ID: id(1), Kind: "series", Name: "Old show", SortName: "b", LibraryID: "lib", AddedAt: day(1)},
		{ID: id(2), Kind: "season", Name: "Season 1", SortName: "c", SeriesID: id(1), LibraryID: "lib", AddedAt: day(1)},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "d", SeriesID: id(1), SeasonID: id(2), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "lib", AddedAt: day(1)},
		{ID: id(4), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(1), SeasonID: id(2), SeasonNumber: &one, EpisodeNumber: &two, LibraryID: "lib", AddedAt: day(20)},
		{ID: id(5), Kind: "series", Name: "New show", SortName: "f", LibraryID: "lib", AddedAt: day(15)},
	}, 1)
	got, err := s.ListCatalog(ctx, ListQuery{Kinds: []string{"movie", "series"}, Sort: "latest", Descending: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{id(1), id(5), id(0)}; !slices.Equal(got, want) {
		t.Errorf("latest = %v, want %v", got, want)
	}
}

// Genres count a library's films and series, and only what the profile may
// see.
func TestCatalogGenres(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "A", SortName: "a", LibraryID: "films", Genres: []string{"Drama", "Crime"}},
		{ID: id(1), Kind: "movie", Name: "B", SortName: "b", LibraryID: "films", Genres: []string{"Drama"}, Tags: []string{"hidden"}},
		{ID: id(2), Kind: "series", Name: "C", SortName: "c", LibraryID: "tv", Genres: []string{"Comedy"}},
	}, 1)
	got, err := s.CatalogGenres(ctx, "films", nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []GenreCount{{"Crime", 1}, {"Drama", 2}}; !slices.Equal(got, want) {
		t.Errorf("films = %v, want %v", got, want)
	}
	got, _ = s.CatalogGenres(ctx, "films", &Access{BlockedTags: []string{"hidden"}})
	if want := []GenreCount{{"Crime", 1}, {"Drama", 1}}; !slices.Equal(got, want) {
		t.Errorf("with a hidden tag = %v, want %v", got, want)
	}
}
