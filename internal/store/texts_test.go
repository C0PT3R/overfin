package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// Texts in another language replace the default's where they're kept;
// where they aren't, the default's stay and the title says so.
func TestTitleTexts(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.SetLanguages(ctx, []string{"fr-CA", "en-CA"}, "fr-CA"); err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{{ID: id(0), Kind: "series", Name: "x", SortName: "x", TMDB: "1399"},
		{ID: id(1), Kind: "movie", Name: "y", SortName: "y", TMDB: "550"}}, 1)
	s.RefreshTitleKeys(ctx)
	now := time.Now()
	det, _ := json.Marshal(TitleDetails{Genres: []string{"Drame"}, Tagline: "L'hiver vient", Runtime: 60,
		Cast: []CastMember{{Name: "Kit Harington", Character: "Jon Neige"}}, Seasons: []SeasonInfo{{Number: 1, Name: "Saison 1", EpisodeCount: 2}}})
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "1399", Name: "Le trône de fer", Overview: "Il y a très longtemps…"}, det, now)
	s.SetSeason(ctx, "1399", Season{Number: 1, Name: "Saison 1", Overview: "La première",
		Episodes: []Episode{{Number: 1, Name: "L'hiver vient"}, {Number: 2, Name: "La route royale"}}}, now)
	s.SetTitleDetails(ctx, Title{Kind: "movie", TMDB: "550", Name: "Fight Club", Overview: "Le narrateur…"}, []byte(`{}`), now)

	en, _ := json.Marshal(TitleDetails{Genres: []string{"Drama"}, Tagline: "Winter is coming",
		Cast: []CastMember{{Name: "Kit Harington", Character: "Jon Snow"}}, Seasons: []SeasonInfo{{Number: 1, Name: "Season 1"}}})
	if err := s.SetTitleTexts(ctx, "en-CA", Title{Kind: "series", TMDB: "1399", Name: "Game of Thrones", Overview: "Seven noble families…"}, en, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeasonTexts(ctx, "en-CA", "1399", Season{Number: 1, Name: "Season 1", Overview: "The first",
		Episodes: []Episode{{Number: 1, Name: "Winter Is Coming"}}}); err != nil {
		t.Fatal(err)
	}

	keys := []string{"series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s1/e1", "series/tmdb/1399/s1/e2", "movie/tmdb/550"}
	got, err := s.TitleInfos(ctx, keys, "2026-10-23", "en-CA")
	if err != nil {
		t.Fatal(err)
	}
	if se := got["series/tmdb/1399"]; se.Name != "Game of Thrones" || se.Overview != "Seven noble families…" || se.Untranslated ||
		!slices.Equal(se.Details.Genres, []string{"Drama"}) || se.Details.Tagline != "Winter is coming" || se.Details.Runtime != 60 ||
		se.Details.Cast[0].Character != "Jon Snow" || se.Details.Seasons[0].Name != "Season 1" || se.Details.Seasons[0].EpisodeCount != 2 {
		t.Errorf("series: %+v", se)
	}
	if s1 := got["series/tmdb/1399/s1"]; s1.Name != "Season 1" || s1.Overview != "The first" || s1.SeriesName != "Game of Thrones" || s1.Untranslated {
		t.Errorf("season: %+v", s1)
	}
	if e := got["series/tmdb/1399/s1/e1"]; e.Name != "Winter Is Coming" || e.SeasonName != "Season 1" || e.Untranslated {
		t.Errorf("episode: %+v", e)
	}
	// An episode whose texts aren't kept: the default's, marked.
	if e := got["series/tmdb/1399/s1/e2"]; e.Name != "La route royale" || !e.Untranslated {
		t.Errorf("episode without texts: %+v", e)
	}
	if m := got["movie/tmdb/550"]; m.Name != "Fight Club" || m.Overview != "Le narrateur…" || !m.Untranslated {
		t.Errorf("film without texts: %+v", m)
	}

	// The default language reads the titles as kept.
	def, _ := s.TitleInfos(ctx, keys, "2026-10-23", "fr-CA")
	if se := def["series/tmdb/1399"]; se.Name != "Le trône de fer" || se.Untranslated {
		t.Errorf("default: %+v", se)
	}

	// The library's titles without texts in en-CA: the film, not the series.
	todo, err := s.TextsToFetch(ctx, "en-CA", now.Add(-time.Hour), 10)
	if err != nil || len(todo) != 1 || todo[0].TMDB != "550" {
		t.Errorf("TextsToFetch = %+v, %v", todo, err)
	}
	if fresh, _ := s.HasTexts(ctx, "en-CA", "series", "1399", now.Add(-time.Hour)); !fresh {
		t.Error("the series' texts aren't fresh")
	}
}
