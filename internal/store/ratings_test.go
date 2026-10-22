package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRatings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	us := []RatingLevel{{"NR", "", 0}, {"G", "", 1}, {"PG", "", 2}, {"PG-13", "", 3}, {"R", "", 4}}
	tv := []RatingLevel{{"TV-Y", "", 1}, {"TV-PG", "", 3}, {"TV-14", "", 4}}
	if err := s.SetRatingScales(ctx, "movie", map[string][]RatingLevel{"US": us}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRatingScales(ctx, "series", map[string][]RatingLevel{"US": tv}); err != nil {
		t.Fatal(err)
	}
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one := 1
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Pixar", SortName: "a", TMDB: "1"},   // G
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "b", TMDB: "2"},    // R
		{ID: id(2), Kind: "movie", Name: "Cut", SortName: "c", TMDB: "3"},     // PG and an R cut
		{ID: id(3), Kind: "movie", Name: "Nothing", SortName: "d", TMDB: "4"}, // fetched, no US rating
		{ID: id(4), Kind: "movie", Name: "Unrated", SortName: "e", TMDB: "5"}, // NR
		{ID: id(5), Kind: "movie", Name: "No TMDB", SortName: "f"},            // nothing to go by
		{ID: id(6), Kind: "movie", Name: "Not yet", SortName: "g", TMDB: "6"}, // not fetched
		{ID: id(7), Kind: "series", Name: "Bluey", SortName: "h", TMDB: "10"}, // TV-Y
		{ID: id(8), Kind: "episode", Name: "Ep", SortName: "i", SeriesID: id(7), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(9), Kind: "movie", Name: "Odd", SortName: "j", TMDB: "7"}, // a rating not on the scale
	}, 1)
	for tmdb, r := range map[string]map[string][]string{
		"1": {"US": {"G"}, "FR": {"18"}}, "2": {"US": {"R"}}, "3": {"US": {"PG", "R"}},
		"4": {"FR": {"U"}}, "5": {"US": {"NR"}}, "7": {"US": {"X"}},
	} {
		if err := s.SetTitleRatings(ctx, RatedTitle{"movie", tmdb}, r, now); err != nil {
			t.Fatal(err)
		}
	}
	s.SetTitleRatings(ctx, RatedTitle{"series", "10"}, map[string][]string{"US": {"TV-Y"}}, now)

	allowed := func(c Ceiling) []string {
		got, err := s.ListCatalog(ctx, ListQuery{Sort: "name", Access: &Access{Ceiling: &c}, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := allowed(Ceiling{"US", "PG", "TV-Y"}); !slices.Equal(got, []string{id(0), id(7), id(8)}) {
		t.Errorf("PG: %v", got)
	}
	if got := allowed(Ceiling{"US", "R", ""}); !slices.Equal(got, []string{id(0), id(1), id(2)}) {
		t.Errorf("R, no series ceiling: %v", got)
	}
	// Nothing for an empty ceiling, a ceiling of "NR", or a country with
	// no scale.
	for _, c := range []Ceiling{{"US", "", ""}, {"US", "NR", ""}, {"FR", "R", "TV-14"}} {
		if got := allowed(c); len(got) != 0 {
			t.Errorf("%+v: %v", c, got)
		}
	}
	if n, err := s.CountCatalog(ctx, ListQuery{Access: &Access{Ceiling: &Ceiling{"US", "PG", "TV-Y"}}}); err != nil || n != 3 {
		t.Errorf("count: %d %v", n, err)
	}
	got, indexed, err := s.AllowedItems(ctx, Access{Ceiling: &Ceiling{"US", "PG", "TV-Y"}}, []string{id(1), id(8), "ffffffffffffffffffffffffffffffff"})
	if err != nil || len(got) != 1 || !got[id(8)] || len(indexed) != 2 {
		t.Errorf("items: %v %v %v", got, indexed, err)
	}
	if ok, err := s.TitleAllowed(ctx, Ceiling{"US", "PG", "TV-Y"}, "movie", "1"); err != nil || !ok {
		t.Errorf("title 1: %v %v", ok, err)
	}
	if ok, _ := s.TitleAllowed(ctx, Ceiling{"US", "PG", "TV-Y"}, "movie", "2"); ok {
		t.Error("title 2 allowed")
	}

	// Titles still to fetch: the unfetched one, then none once fetched.
	todo, err := s.TitlesToRate(ctx, now.Add(-time.Hour), 50)
	if err != nil || len(todo) != 1 || todo[0] != (RatedTitle{"movie", "6"}) {
		t.Errorf("to rate: %v %v", todo, err)
	}
	if f, total, err := s.RatingProgress(ctx); err != nil || f != 7 || total != 8 {
		t.Errorf("progress: %d/%d %v", f, total, err)
	}
	// Kept five months at most, then hidden again.
	if err := s.PurgeRatings(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := allowed(Ceiling{"US", "R", "TV-14"}); len(got) != 0 {
		t.Errorf("after the purge: %v", got)
	}

	// A new ratings country clears kids profiles' ceilings.
	acct, _, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	kid, err := s.AddProfile(ctx, acct.ID, NewProfile{Name: "Kid", Kind: "kids", MovieCeiling: "PG", SeriesCeiling: "TV-Y",
		JellyfinUserID: "u2", JellyfinUsername: "of-2", JellyfinToken: "t2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := s.RatingsCountry(ctx); c != "US" {
		t.Errorf("default country: %s", c)
	}
	s.SetRatingsCountry(ctx, "US") // the same: nothing changes
	if p, _ := s.Profile(ctx, acct.ID, kid.ID); p.MovieCeiling != "PG" || p.SeriesCeiling != "TV-Y" {
		t.Errorf("same country: %+v", p)
	}
	if err := s.SetRatingsCountry(ctx, "FR"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Profile(ctx, acct.ID, kid.ID); p.MovieCeiling != "" || p.SeriesCeiling != "" {
		t.Errorf("new country: %+v", p)
	}
}
