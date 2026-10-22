package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseTitleKey(t *testing.T) {
	for key, want := range map[string]TitleRef{
		"movie/tmdb/949":                   {Kind: "movie", Source: "tmdb", ID: "949"},
		"series/tvdb/81189/s2":             {Kind: "season", Source: "tvdb", ID: "81189", Season: 2},
		"series/tmdb/1399/s0/e12":          {Kind: "episode", Source: "tmdb", ID: "1399", Season: 0, Episode: 12},
		"item/" + strings.Repeat("ab", 16): {Kind: "item", ID: strings.Repeat("ab", 16)},
	} {
		got, ok := ParseTitleKey(key)
		if !ok || got != want || got.Key() != key {
			t.Errorf("%s: %+v %v", key, got, ok)
		}
	}
	for _, bad := range []string{"movie/tmdb/949/s1", "show/tmdb/1", "movie/tmdb/../x", "item/xyz", "series/tmdb/1/e2"} {
		if _, ok := ParseTitleKey(bad); ok {
			t.Errorf("%s parsed", bad)
		}
	}
}

// A series the library has part of: its page and images from TMDB, its
// own episodes counted against those aired, a season it lacks entirely,
// and an episode not aired yet.
func TestTitleInfos(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "Jellyfin's name", SortName: "a", TMDB: "1399"},
		{ID: id(1), Kind: "season", Name: "S1", SortName: "s", SeriesID: id(0), SeasonNumber: n(1)},
		{ID: id(2), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(0), SeasonID: id(1), SeasonNumber: n(1), EpisodeNumber: n(1)},
		{ID: id(3), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(0), SeasonID: id(1), SeasonNumber: n(1), EpisodeNumber: n(2)},
		{ID: id(4), Kind: "movie", Name: "home video.mkv", SortName: "h", Year: 2020},
	}, 1)
	s.RefreshTitleKeys(ctx)
	det, _ := json.Marshal(TitleDetails{Genres: []string{"Drame"}, Seasons: []SeasonInfo{{Number: 1, EpisodeCount: 3}, {Number: 2, EpisodeCount: 2}}})
	now := time.Now()
	s.SetTitleDetails(ctx, Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer", Year: 2011, Poster: "/poster1234.jpg"}, det, now)
	s.SetSeason(ctx, "1399", Season{Number: 1, Name: "Saison 1", Episodes: []Episode{
		{Number: 1, Name: "L'hiver vient", AirDate: "2011-04-17"}, {Number: 2, Name: "La route", AirDate: "2011-04-24"},
		{Number: 3, Name: "Lord Snow", AirDate: "2011-05-01"}}}, now)
	s.SetSeason(ctx, "1399", Season{Number: 2, Name: "Saison 2", Episodes: []Episode{
		{Number: 1, Name: "Le Nord", AirDate: "2012-04-01"}, {Number: 2, Name: "Plus tard", AirDate: "2099-01-01"}}}, now)

	keys := []string{"series/tmdb/1399", "series/tmdb/1399/s1", "series/tmdb/1399/s2", "series/tmdb/1399/s2/e1",
		"series/tmdb/1399/s1/e2", "item/" + id(4)}
	got, err := s.TitleInfos(ctx, keys, "2026-10-22")
	if err != nil {
		t.Fatal(err)
	}
	if se := got["series/tmdb/1399"]; se.Name != "Le Trône de fer" || !se.Known || se.Have != 2 || se.Aired != 4 ||
		!slices.Equal(se.Items, []string{id(0)}) || se.Poster != "/poster1234.jpg" {
		t.Errorf("series: %+v", se)
	}
	if s1 := got["series/tmdb/1399/s1"]; s1.Name != "Saison 1" || s1.SeriesName != "Le Trône de fer" || s1.Have != 2 || s1.Aired != 3 {
		t.Errorf("season 1: %+v", s1)
	}
	if s2 := got["series/tmdb/1399/s2"]; s2.Have != 0 || s2.Aired != 1 || len(s2.Items) != 0 || s2.Poster != "/poster1234.jpg" {
		t.Errorf("season 2 (not in the library): %+v", s2)
	}
	if e := got["series/tmdb/1399/s2/e1"]; e.Name != "Le Nord" || len(e.Items) != 0 || e.Year != 2012 {
		t.Errorf("missing episode: %+v", e)
	}
	if e := got["series/tmdb/1399/s1/e2"]; e.Name != "La route" || !slices.Equal(e.Items, []string{id(3)}) {
		t.Errorf("episode in the library: %+v", e)
	}
	if h := got["item/"+id(4)]; h.Name != "home video.mkv" || h.Known || h.Year != 2020 {
		t.Errorf("unidentified file: %+v", h)
	}
	eps, _ := s.SeasonEpisodes(ctx, TitleRef{Kind: "season", Source: "tmdb", ID: "1399", Season: 2})
	if !slices.Equal(eps, []int{1, 2}) {
		t.Errorf("season 2's episodes: %v", eps)
	}
	seasons, _ := s.SeriesSeasons(ctx, TitleRef{Kind: "series", Source: "tmdb", ID: "1399"}, []SeasonInfo{{Number: 1}, {Number: 2}})
	if !slices.Equal(seasons, []int{1, 2}) {
		t.Errorf("seasons: %v", seasons)
	}
}

// Continue Watching in titles: what's in the middle, and each followed
// series' next episode, a library's or not.
func TestContinueTitles(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	now := time.Now()
	for _, se := range []Season{
		{Number: 1, Episodes: []Episode{{Number: 1, AirDate: "2011-04-17"}, {Number: 2, AirDate: "2011-04-24"}}},
		{Number: 2, Episodes: []Episode{{Number: 1, AirDate: "2012-04-01"}, {Number: 2, AirDate: "2099-01-01"}}},
	} {
		s.SetSeason(ctx, "1399", se, now)
	}
	s.SetSeason(ctx, "2", Season{Number: 1, Episodes: []Episode{{Number: 1, AirDate: "2020-01-01"}, {Number: 2, AirDate: "2020-01-08"}}}, now)
	// Series 1399: season 1 watched; season 2, episode 1 (no library has
	// it) is next. Series 2: episode 1 in the middle. A film in the middle.
	s.SetPlayed(ctx, prof.ID, map[string]string{"series/tmdb/1399/s1/e1": "", "series/tmdb/1399/s1/e2": ""}, true, now.Add(-2*time.Hour))
	s.RecordPlay(ctx, prof.ID, "series/tmdb/2/s1/e1", "", 60_000, false, now.Add(-time.Hour))
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/949", "", 60_000, false, now.Add(-time.Minute))

	got, err := s.ContinueTitles(ctx, prof.ID, "2026-10-22", 10)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"movie/tmdb/949", "series/tmdb/2/s1/e1", "series/tmdb/1399/s2/e1"}; !slices.Equal(got, want) {
		t.Errorf("continue = %v, want %v", got, want)
	}
	// Season 2's first episode watched: the next hasn't aired, so the
	// series has nothing next.
	s.SetPlayed(ctx, prof.ID, map[string]string{"series/tmdb/1399/s2/e1": ""}, true, now)
	if got, _ := s.ContinueTitles(ctx, prof.ID, "2026-10-22", 10); len(got) != 2 {
		t.Errorf("nothing aired next: %v", got)
	}
}

// A title's certification is the most mature of its releases' in the
// ratings country; a season's is its series'.
func TestTitleCertification(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	s.SetRatingScales(ctx, "movie", map[string][]RatingLevel{"US": {{Certification: "G", Position: 1}, {Certification: "PG-13", Position: 3}}})
	s.SetTitleRatings(ctx, RatedTitle{"movie", "1"}, map[string][]string{"US": {"G", "PG-13"}, "FR": {"12"}}, "", "", time.Now())
	s.SetTitleRatings(ctx, RatedTitle{"series", "2"}, map[string][]string{"US": {"TV-MA"}}, "", "", time.Now())
	got, err := s.TitleInfos(ctx, []string{"movie/tmdb/1", "series/tmdb/2/s1", "movie/tmdb/3"}, "2026-10-22")
	if err != nil {
		t.Fatal(err)
	}
	if c := got["movie/tmdb/1"].Certification; c != "PG-13" {
		t.Errorf("film: %q", c)
	}
	if c := got["series/tmdb/2/s1"].Certification; c != "TV-MA" {
		t.Errorf("season: %q", c)
	}
	if c := got["movie/tmdb/3"].Certification; c != "" {
		t.Errorf("unrated: %q", c)
	}
}

// The library's titles: films, series, the seasons and episodes it has;
// not files no one identified.
func TestLibraryTitleKeys(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	n := func(i int) *int { return &i }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "series", Name: "A", SortName: "a", TMDB: "1"},
		{ID: id(1), Kind: "episode", Name: "E", SortName: "e", SeriesID: id(0), SeasonNumber: n(2), EpisodeNumber: n(3)},
		{ID: id(2), Kind: "movie", Name: "M", SortName: "m", TMDB: "3"},
		{ID: id(3), Kind: "movie", Name: "Unknown", SortName: "u"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	keys, err := s.LibraryTitleKeys(ctx)
	slices.Sort(keys)
	if want := []string{"movie/tmdb/3", "series/tmdb/1", "series/tmdb/1/s2", "series/tmdb/1/s2/e3"}; err != nil || !slices.Equal(keys, want) {
		t.Errorf("keys: %v %v", keys, err)
	}
}
