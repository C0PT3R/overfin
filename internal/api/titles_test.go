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

	// Lists give titles: the library's, what's new, Continue Watching.
	var page struct {
		Items      []testTitle
		TotalCount *int
	}
	if code := get(t, srv, "/api/v1/titles?kind=series", &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].ID != "title_series_tmdb_1399" || page.TotalCount == nil || *page.TotalCount != 1 {
		t.Errorf("series listing: %d %+v", code, page)
	}
	page.Items = nil
	if code := get(t, srv, "/api/v1/titles/latest", &page); code != 200 || len(page.Items) != 1 || page.Items[0].Kind != "series" {
		t.Errorf("latest: %d %+v", code, page.Items)
	}
	// Season 1's two episodes the library has, watched (season 2 not):
	// next is the third, which it doesn't have.
	put(t, srv, "DELETE", "/api/v1/titles/title_series_tmdb_1399_s2/played", "")
	for _, e := range []string{"e1", "e2"} {
		put(t, srv, "PUT", "/api/v1/titles/title_series_tmdb_1399_s1_"+e+"/played", "")
	}
	page.Items = nil
	if code := get(t, srv, "/api/v1/titles/continue", &page); code != 200 || len(page.Items) != 1 ||
		page.Items[0].ID != "title_series_tmdb_1399_s1_e3" || page.Items[0].Availability != "missing" {
		t.Errorf("continue: %d %+v", code, page.Items)
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

// Playback asks for a title; Overfin picks the copy. A title no library
// has doesn't play.
func TestPlayATitle(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Heat", SortName: "heat", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	det, _ := json.Marshal(store.TitleDetails{})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "949", Name: "Heat (TMDB)"}, det, time.Now())
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Pas ici"}, det, time.Now())

	code, body := post(t, srv, "/api/v1/playback", `{"titleId":"title_movie_tmdb_949","capabilities":{}}`)
	var session struct {
		Title *struct{ ID, Name string }
	}
	json.Unmarshal([]byte(body), &session)
	if code != 201 || session.Title == nil || session.Title.ID != "title_movie_tmdb_949" || session.Title.Name != "Heat (TMDB)" {
		t.Errorf("play a title: %d %s", code, body)
	}
	if code, _ := post(t, srv, "/api/v1/playback", `{"titleId":"title_movie_tmdb_100","capabilities":{}}`); code != 404 {
		t.Errorf("a title no library has: %d", code)
	}
	if code, _ := post(t, srv, "/api/v1/playback", fmt.Sprintf(`{"titleId":"title_movie_tmdb_949","itemId":"item_%032x"}`, 1)); code != 400 {
		t.Errorf("both a title and a copy: %d", code)
	}
}
