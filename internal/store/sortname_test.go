package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSortName(t *testing.T) {
	for in, want := range map[string]string{
		"L'Odyssée": "odyssee", "The Office": "office", "Évasion": "evasion", "Les Misérables": "miserables",
		"A": "a", "Œdipe": "oedipe", "  Toy Story 4": "toy story 4", "La": "la",
	} {
		if got := SortName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The library is searched by TMDB's names, accents and case aside, whole
// names first; a file TMDB doesn't know, by its own name.
func TestSearchLibrary(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Escape Plan", SortName: "e", TMDB: "1"},
		{ID: id(1), Kind: "movie", Name: "x", SortName: "x", TMDB: "2"},
		{ID: id(2), Kind: "series", Name: "Les Bougon (QC)", SortName: "b"},
		{ID: id(3), Kind: "movie", Name: "y", SortName: "y", TMDB: "3"},
	}, 1)
	s.RefreshTitleKeys(ctx)
	s.PutTitles(ctx, []Title{{Kind: "movie", TMDB: "1", Name: "Évasion"}, {Kind: "movie", TMDB: "2", Name: "La grande évasion"},
		{Kind: "movie", TMDB: "3", Name: "Autre chose"}}, time.Now())
	if got, _ := s.SearchLibrary(ctx, "EVASION", 10); !slices.Equal(got, []string{"movie/tmdb/1", "movie/tmdb/2"}) {
		t.Errorf("évasion: %v", got)
	}
	if got, _ := s.SearchLibrary(ctx, "grande evas", 10); !slices.Equal(got, []string{"movie/tmdb/2"}) {
		t.Errorf("words: %v", got)
	}
	if got, _ := s.SearchLibrary(ctx, "bougon", 10); !slices.Equal(got, []string{"item/" + id(2)}) {
		t.Errorf("a file's own name: %v", got)
	}
	if got, _ := s.SearchLibrary(ctx, "100%_", 10); len(got) != 0 {
		t.Errorf("wildcards are letters: %v", got)
	}
}
