package store

import (
	"context"
	"testing"
	"time"
)

// Texts in other languages are TMDB's data too: kept five months at most,
// a series' seasons and episodes with it, and all gone with TMDB.
func TestTextsRetention(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	count := func(table string) (n int) {
		s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		return n
	}
	season := Season{Number: 1, Name: "Season 1", Episodes: []Episode{{Number: 1, Name: "Pilot"}}}
	s.SetTitleTexts(ctx, "en-CA", Title{Kind: "series", TMDB: "1", Name: "Old"}, []byte(`{}`), now.Add(-200*24*time.Hour))
	s.SetSeasonTexts(ctx, "en-CA", "1", season)
	s.SetTitleTexts(ctx, "en-CA", Title{Kind: "series", TMDB: "2", Name: "Fresh"}, []byte(`{}`), now)
	s.SetSeasonTexts(ctx, "en-CA", "2", season)
	if _, err := s.PurgeTitles(ctx, now.Add(-150*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count("catalog_title_texts") != 1 || count("catalog_season_texts") != 1 || count("catalog_episode_texts") != 1 {
		t.Errorf("after the purge: %d titles, %d seasons, %d episodes",
			count("catalog_title_texts"), count("catalog_season_texts"), count("catalog_episode_texts"))
	}
	if err := s.ForgetTMDB(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"catalog_title_texts", "catalog_season_texts", "catalog_episode_texts", "tmdb_found"} {
		if n := count(table); n != 0 {
			t.Errorf("%s after forgetting TMDB: %d", table, n)
		}
	}
}
