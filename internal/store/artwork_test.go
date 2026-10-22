package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArtwork(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "949"},
		{ID: id(1), Kind: "series", Name: "Show", SortName: "b", TMDB: "949"}, // the same number as the film
		{ID: id(2), Kind: "season", Name: "S1", SortName: "c", SeriesID: id(1), SeasonNumber: &one},
		{ID: id(3), Kind: "season", Name: "S2", SortName: "d", SeriesID: id(1), SeasonNumber: &two},
		{ID: id(4), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(1), SeasonID: id(2), SeasonNumber: &one, EpisodeNumber: &one},
	}, 1)
	s.RefreshTitleKeys(ctx)
	now := time.Now()

	todo, err := s.ArtworkToFetch(ctx, now, 10)
	if err != nil || len(todo) != 2 {
		t.Fatalf("to fetch: %+v %v", todo, err)
	}
	for _, target := range todo {
		// A film has no seasons, even one sharing its number with a series.
		if target.Kind == "movie" && len(target.Seasons) != 0 || target.Kind == "series" && len(target.Seasons) != 2 {
			t.Errorf("target: %+v", target)
		}
	}
	series := ArtworkTarget{Kind: "series", TMDB: "949"}
	err = s.SetArtwork(ctx, series, map[string]map[string]string{
		"series/tmdb/949":       {"poster": "/seriesposter.jpg", "backdrop": "/seriesbackdr.jpg", "logo": ""},
		"series/tmdb/949/s1":    {"poster": "/season1post.jpg"},
		"series/tmdb/949/s1/e1": {"still": "/episode1stl.jpg"},
		"movie/tmdb/949":        {"poster": "/notthisone.jpg"}, // not the target's: ignored
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	art, err := s.Artwork(ctx, []string{"series/tmdb/949", "series/tmdb/949/s1/e1", "movie/tmdb/949"})
	if err != nil || art["series/tmdb/949"]["poster"] != "/seriesposter.jpg" || art["series/tmdb/949/s1/e1"]["still"] != "/episode1stl.jpg" ||
		art["movie/tmdb/949"] != nil || len(art["series/tmdb/949"]) != 2 {
		t.Errorf("artwork: %v %v", art, err)
	}
	// A series' own image belongs to all its items; an episode's, to it.
	if got, _ := s.ArtworkItems(ctx, "poster", "/seriesposter.jpg", 10); len(got) != 4 {
		t.Errorf("series poster's items: %v", got)
	}
	if got, _ := s.ArtworkItems(ctx, "still", "/episode1stl.jpg", 10); !slices.Equal(got, []string{id(4)}) {
		t.Errorf("still's items: %v", got)
	}
	if got, _ := s.ArtworkItems(ctx, "poster", "/invented12.jpg", 10); len(got) != 0 {
		t.Errorf("invented path: %v", got)
	}
	if todo, _ := s.ArtworkToFetch(ctx, now.Add(-time.Hour), 10); len(todo) != 1 || todo[0].Kind != "movie" {
		t.Errorf("left to fetch: %+v", todo)
	}

	// A new language: everything is fetched again.
	if l, _ := s.TMDBLanguage(ctx); l != "en" {
		t.Errorf("default language: %s", l)
	}
	s.SetTMDBLanguage(ctx, "fr")
	if l, _ := s.TMDBLanguage(ctx); l != "fr" {
		t.Errorf("language: %s", l)
	}
	if todo, _ := s.ArtworkToFetch(ctx, now.Add(-time.Hour), 10); len(todo) != 2 {
		t.Errorf("after a new language: %+v", todo)
	}
	// Kept five months at most.
	s.SetArtwork(ctx, series, map[string]map[string]string{"series/tmdb/949": {"poster": "/seriesposter.jpg"}}, now)
	if err := s.PurgeArtwork(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if art, _ := s.Artwork(ctx, []string{"series/tmdb/949"}); len(art) != 0 {
		t.Errorf("after the purge: %v", art)
	}
}
