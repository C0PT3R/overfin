package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func fakeTMDB(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" || strings.Contains(r.URL.RawQuery, "token") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/3/trending/movie/week":
			io.WriteString(w, `{"page":1,"total_pages":900,"results":[
				{"id":949,"media_type":"movie","title":"Heat <b>!</b>","release_date":"1995-12-15","poster_path":"/abcdefgh1.jpg"},
				{"id":100,"media_type":"movie","title":"Bad","poster_path":"../../etc/passwd"},
				{"id":1399,"media_type":"tv","name":"A series"}]}`)
		case "/3/tv/on_the_air":
			io.WriteString(w, `{"page":2,"total_pages":2,"results":[{"id":1399,"name":"Thrones","first_air_date":"2011-04-17"}]}`)
		case "/3/movie/949":
			if r.URL.Query().Get("append_to_response") != "credits,release_dates" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id":949,"title":"Heat","genres":[{"name":"Crime"}],
				"credits":{"cast":[{"name":"Al Pacino","character":"Hanna"}]},
				"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"R"},{"certification":""},{"certification":"R"}]},
					{"iso_3166_1":"FR","release_dates":[{"certification":"12"}]},{"iso_3166_1":"bad","release_dates":[{"certification":"X"}]}]}}`)
		case "/3/tv/1399":
			io.WriteString(w, `{"id":1399,"name":"Thrones","seasons":[{"season_number":1,"name":"Season 1","episode_count":10}],
				"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-MA"}]}}`)
		case "/3/movie/550":
			if r.URL.Query().Get("append_to_response") != "images" || r.URL.Query().Get("language") != "fr" ||
				r.URL.Query().Get("include_image_language") != "fr,null,en" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id":550,"poster_path":"/posterfr12.jpg","backdrop_path":"/backdrop12.jpg",
				"images":{"logos":[{"file_path":"/logoen1234.png","iso_639_1":"en"},{"file_path":"/logofr1234.png","iso_639_1":"fr"},
					{"file_path":"/logo.svg","iso_639_1":"fr"}]}}`)
		case "/3/tv/1399/season/1":
			io.WriteString(w, `{"poster_path":"/season1abc.jpg","episodes":[{"episode_number":1,"still_path":"/still1abcd.jpg"},
				{"episode_number":2,"still_path":null},{"episode_number":3,"still_path":"../x.jpg"}]}`)
		case "/3/certification/movie/list":
			io.WriteString(w, `{"certifications":{"US":[{"certification":"G","meaning":"All ages","order":1},{"certification":"R","order":4}]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/3", "token")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClient(t *testing.T) {
	c := fakeTMDB(t)
	ctx := context.Background()

	p, err := c.Discover(ctx, "trending", "movie", 1)
	if err != nil {
		t.Fatal(err)
	}
	// The series is left out of a film list; an unsafe poster is dropped;
	// TMDB's 500 pages at most.
	if len(p.Titles) != 2 || p.TotalPages != 500 || p.Titles[0].Name != "Heat !" || p.Titles[0].Year != 1995 ||
		p.Titles[0].Poster != "/abcdefgh1.jpg" || p.Titles[1].Poster != "" {
		t.Errorf("trending: %+v", p)
	}
	if p, err := c.Discover(ctx, "upcoming", "series", 2); err != nil || len(p.Titles) != 1 || p.Titles[0].Kind != "series" || p.Titles[0].Year != 2011 {
		t.Errorf("upcoming series: %+v %v", p, err)
	}

	d, err := c.Details(ctx, "movie", 949)
	if err != nil || d.Name != "Heat" || !slices.Equal(d.Genres, []string{"Crime"}) || len(d.Cast) != 1 {
		t.Errorf("details: %+v %v", d, err)
	}
	if d, err := c.Details(ctx, "series", 1399); err != nil || len(d.Seasons) != 1 || d.Seasons[0].EpisodeCount != 10 {
		t.Errorf("series details: %+v %v", d, err)
	}
	r, err := c.Ratings(ctx, "movie", 949)
	if err != nil || !slices.Equal(r["US"], []string{"R"}) || !slices.Equal(r["FR"], []string{"12"}) || len(r) != 2 {
		t.Errorf("film ratings: %v %v", r, err)
	}
	if r, err := c.Ratings(ctx, "series", 1399); err != nil || !slices.Equal(r["US"], []string{"TV-MA"}) {
		t.Errorf("series ratings: %v %v", r, err)
	}
	scales, err := c.Certifications(ctx, "movie")
	if err != nil || len(scales["US"]) != 2 || scales["US"][0].Meaning != "All ages" {
		t.Errorf("scales: %v %v", scales, err)
	}

	// Artwork in the client's language: the French logo before the English
	// one; an SVG never.
	c.Language = func() string { return "fr" }
	art, err := c.Artwork(ctx, "movie", 550)
	if err != nil || art.Poster != "/posterfr12.jpg" || art.Backdrop != "/backdrop12.jpg" || art.Logo != "/logofr1234.png" {
		t.Errorf("artwork: %+v %v", art, err)
	}
	season, err := c.Season(ctx, 1399, 1)
	if err != nil || season.Poster != "/season1abc.jpg" || len(season.Stills) != 1 || season.Stills[1] != "/still1abcd.jpg" {
		t.Errorf("season: %+v %v", season, err)
	}
	c.Language = nil

	if _, err := c.Details(ctx, "movie", 12345); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown title: %v", err)
	}
	bad, _ := New(c.base.String(), "wrong")
	if _, err := bad.Details(ctx, "movie", 949); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("wrong key: %v", err)
	}
}
