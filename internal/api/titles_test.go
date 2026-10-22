package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

type testTitle struct {
	ID, Kind, Name, Availability string
	Year                         int
	Playable                     bool
	Series                       *struct{ ID, Name string }
	EpisodeCount                 *struct{ InLibrary, Aired int }
	Images                       *struct{ Primary *struct{ URL string } }
	UserState                    *struct{ Played, Favorite bool }
	Overview                     string
	Genres                       []string
	Seasons, Episodes            []testTitle
}

// Titles, whether a library has them or not: one page, TMDB's data only,
// what's there and what isn't, and marks on titles no library has.
func TestTitles(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	n := func(i int) *int { return &i }
	// The library has a series' first two episodes, and a film.
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(1), Kind: "series", Name: "Jellyfin's name", SortName: "a", TMDB: "1399"},
		{ID: id(2), Kind: "season", Name: "S1", SortName: "s", SeriesID: id(1), SeasonNumber: n(1)},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "e", SeriesID: id(1), SeasonID: id(2), SeasonNumber: n(1), EpisodeNumber: n(1)},
		{ID: id(4), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(1), SeasonID: id(2), SeasonNumber: n(1), EpisodeNumber: n(2)},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	now := time.Now()
	det, _ := json.Marshal(store.TitleDetails{Genres: []string{"Drame"}, Seasons: []store.SeasonInfo{{Number: 1, EpisodeCount: 3}, {Number: 2, EpisodeCount: 2}}})
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer", Year: 2011, Overview: "Neuf familles.", Poster: "/poster1234.jpg"}, det, now)
	film, _ := json.Marshal(store.TitleDetails{Genres: []string{"Crime"}})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Pas ici", Year: 2024}, film, now)
	st.SetSeason(ctx, "1399", store.Season{Number: 1, Name: "Saison 1", Episodes: []store.Episode{
		{Number: 1, Name: "L'hiver vient", AirDate: "2011-04-17"}, {Number: 2, Name: "La route", AirDate: "2011-04-24"},
		{Number: 3, Name: "Lord Snow", AirDate: "2011-05-01"}}}, now)
	st.SetSeason(ctx, "1399", store.Season{Number: 2, Name: "Saison 2", Episodes: []store.Episode{
		{Number: 1, Name: "Le Nord", AirDate: "2012-04-01"}, {Number: 2, Name: "Plus tard", AirDate: "2099-01-01"}}}, now)

	var series testTitle
	if code := get(t, srv, "/api/v1/titles/title_series_tmdb_1399", &series); code != 200 {
		t.Fatalf("series: %d", code)
	}
	if series.Name != "Le Trône de fer" || series.Overview != "Neuf familles." || series.Availability != "partiallyAvailable" ||
		series.EpisodeCount == nil || series.EpisodeCount.InLibrary != 2 || series.EpisodeCount.Aired != 4 || len(series.Seasons) != 2 ||
		series.Images == nil || series.Images.Primary.URL != "/api/v1/images/tmdbart_poster_poster1234_jpg" {
		t.Errorf("series: %+v", series)
	}
	if s1, s2 := series.Seasons[0], series.Seasons[1]; s1.Availability != "partiallyAvailable" || s1.Name != "Saison 1" ||
		s2.Availability != "missing" || s2.ID != "title_series_tmdb_1399_s2" || s2.Series.Name != "Le Trône de fer" {
		t.Errorf("seasons: %+v", series.Seasons)
	}

	// A season no library has, browsed: its episodes, none playable.
	var s2 testTitle
	if code := get(t, srv, "/api/v1/titles/title_series_tmdb_1399_s2", &s2); code != 200 || len(s2.Episodes) != 2 {
		t.Fatalf("season 2: %d %+v", code, s2)
	}
	if e := s2.Episodes[0]; e.Name != "Le Nord" || e.Availability != "missing" || e.Playable || e.Kind != "episode" {
		t.Errorf("missing episode: %+v", e)
	}
	var s1 testTitle
	get(t, srv, "/api/v1/titles/title_series_tmdb_1399_s1", &s1)
	if len(s1.Episodes) != 3 || !s1.Episodes[0].Playable || s1.Episodes[2].Playable {
		t.Errorf("season 1: %+v", s1.Episodes)
	}

	// Marks on titles no library has: season 2 watched marks its aired
	// episode, not the one to come.
	if code, body := put(t, srv, "PUT", "/api/v1/titles/title_series_tmdb_1399_s2/played", ""); code != 200 {
		t.Fatalf("mark season 2: %d %s", code, body)
	}
	s2 = testTitle{}
	get(t, srv, "/api/v1/titles/title_series_tmdb_1399_s2", &s2)
	if s2.UserState == nil || !s2.UserState.Played || s2.Episodes[0].UserState == nil || !s2.Episodes[0].UserState.Played ||
		s2.Episodes[1].UserState != nil {
		t.Errorf("season 2 watched: %+v", s2)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/titles/title_movie_tmdb_100/favorite", ""); code != 200 {
		t.Errorf("favorite, not in a library: %d", code)
	}
	var missing testTitle
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_100", &missing); code != 200 || missing.Availability != "missing" ||
		missing.Playable || missing.UserState == nil || !missing.UserState.Favorite {
		t.Errorf("film no library has: %d %+v", code, missing)
	}

	// Nothing known, or not a title.
	for _, bad := range []string{"title_movie_tmdb_555", "title_movie_tmdb_949_s1", "title_movie/tmdb/1", "item_" + id(1), "title_series_tmdb_1399_s9_e1"} {
		if code := get(t, srv, "/api/v1/titles/"+bad, nil); code != 404 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	// A kids profile sees a title no library has only once it's rated
	// within its limits.
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","movieCeiling":"G","seriesCeiling":"TV-Y"}`)
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	if code, body := post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate kid: %d %s", code, body)
	}
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_100", nil); code != 404 {
		t.Errorf("unrated, for a kid: %d", code)
	}
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "100"}, map[string][]string{"US": {"G"}}, now)
	if code := get(t, srv, "/api/v1/titles/title_movie_tmdb_100", nil); code != 200 {
		t.Errorf("rated G, for a kid: %d", code)
	}
}
