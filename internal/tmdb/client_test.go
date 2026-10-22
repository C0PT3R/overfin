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
				"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"R","type":1,"release_date":"1995-11-01T00:00:00.000Z"},
					{"certification":"","type":3,"release_date":"1995-12-15T00:00:00.000Z"},{"certification":"R","type":4,"release_date":"1996-06-04T00:00:00.000Z"}]},
					{"iso_3166_1":"FR","release_dates":[{"certification":"12","type":5,"release_date":"1996-05-20T00:00:00.000Z"}]},
					{"iso_3166_1":"bad","release_dates":[{"certification":"X","type":3,"release_date":"soon"}]}]}}`)
		case "/3/tv/1399":
			io.WriteString(w, `{"id":1399,"name":"Thrones","first_air_date":"2011-04-17","seasons":[{"season_number":1,"name":"Season 1","episode_count":10}],
				"content_ratings":{"results":[{"iso_3166_1":"US","rating":"TV-MA"}]}}`)
		case "/3/movie/550":
			if r.URL.Query().Get("append_to_response") != "credits,images,alternative_titles,translations" || r.URL.Query().Get("language") != "fr" ||
				r.URL.Query().Get("include_image_language") != "fr,null,en" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id":550,"title":"Fight Club","release_date":"1999-10-15","runtime":139,"tagline":"Mischief.","vote_average":8.4,
				"original_title":"Fight Club","original_language":"en",
				"alternative_titles":{"titles":[{"title":"El club de la lucha"},{"title":"Fight Club"}]},
				"translations":{"translations":[{"data":{"title":"Club <i>de</i> combat"}},{"data":{"title":""}}]},
				"overview":"An insomniac.","genres":[{"name":"Drame"}],
				"credits":{"cast":[{"id":819,"name":"Edward Norton","character":"Narrator","profile_path":"/norton1234.jpg"}]},
				"poster_path":"/posterfr12.jpg","backdrop_path":"/backdrop12.jpg",
				"images":{"logos":[{"file_path":"/logoen1234.png","iso_639_1":"en"},{"file_path":"/logofr1234.png","iso_639_1":"fr"},
					{"file_path":"/logo.svg","iso_639_1":"fr"}]}}`)
		case "/3/person/1245":
			io.WriteString(w, `{"id":1245,"name":"Scarlett <b>Johansson</b>","biography":"Actrice.","birthday":"1984-11-22",
				"place_of_birth":"New York","profile_path":"/scarlett123.jpg","known_for_department":"Acting",
				"combined_credits":{"cast":[{"id":1726,"media_type":"movie","title":"Iron Man 2","release_date":"2010-04-28","character":"Natasha"},
					{"id":9,"media_type":"tv","name":"SNL","first_air_date":"1975-10-11","character":"Host"},
					{"id":3,"media_type":"person","name":"?"}],
					"crew":[{"id":5,"media_type":"movie","title":"North Star","release_date":"2025-01-01","job":"Director"}]}}`)
		case "/3/search/multi":
			if r.URL.Query().Get("query") != "evasion" || r.URL.Query().Get("include_adult") != "false" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"results":[{"id":1,"media_type":"movie","title":"Évasion","release_date":"2013-10-18"},
				{"id":2,"media_type":"tv","name":"Prison Break","first_air_date":"2005-08-29"},
				{"id":3,"media_type":"person","name":"Sly","profile_path":"/sly1234567.jpg","known_for_department":"Acting"},
				{"id":4,"media_type":"collection","name":"?"}]}`)
		case "/3/find/tt0944947":
			if r.URL.Query().Get("external_source") != "imdb_id" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"movie_results":[],"tv_results":[{"id":1399}]}`)
		case "/3/tv/1400":
			if r.URL.Query().Get("append_to_response") != "aggregate_credits,images,alternative_titles,translations" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			io.WriteString(w, `{"id":1400,"name":"Seinfeld","first_air_date":"1989-07-05","original_name":"Seinfeld","original_language":"english",
				"alternative_titles":{"results":[{"title":"Les Seinfeld"}]},"translations":{"translations":[{"data":{"name":"Seinfeld"}}]},
				"credits":{"cast":[]},
				"aggregate_credits":{"cast":[{"name":"Jerry Seinfeld","profile_path":"/jerry12345.jpg","roles":[{"character":"Jerry"}]},
					{"name":"Guest","roles":[]}]}}`)
		case "/3/tv/1399/season/1":
			io.WriteString(w, `{"season_number":1,"name":"Saison 1","overview":"Winter.","air_date":"2011-04-17",
				"poster_path":"/season1abc.jpg","episodes":[
				{"episode_number":1,"name":"Winter <b>is</b> coming","air_date":"2011-04-17","runtime":62,"still_path":"/still1abcd.jpg"},
				{"episode_number":2,"name":"The Kingsroad","air_date":"soon","still_path":null},
				{"episode_number":3,"still_path":"../x.jpg"},{"episode_number":3,"name":"Twice"}]}`)
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
	rel, err := c.Release(ctx, "movie", 949)
	if err != nil {
		t.Fatal(err)
	}
	// Festivals aside, in theatres first; at home first on disc, in France.
	if r := rel.Ratings; !slices.Equal(r["US"], []string{"R"}) || !slices.Equal(r["FR"], []string{"12"}) || len(r) != 2 ||
		rel.Released != "1995-12-15" || rel.AtHome != "1996-05-20" {
		t.Errorf("film release: %+v", rel)
	}
	if rel, err := c.Release(ctx, "series", 1399); err != nil || !slices.Equal(rel.Ratings["US"], []string{"TV-MA"}) ||
		rel.Released != "2011-04-17" || rel.AtHome != "2011-04-17" {
		t.Errorf("series release: %+v %v", rel, err)
	}
	scales, err := c.Certifications(ctx, "movie")
	if err != nil || len(scales["US"]) != 2 || scales["US"][0].Meaning != "All ages" {
		t.Errorf("scales: %v %v", scales, err)
	}

	// Artwork in the client's language: the French logo before the English
	// one; an SVG never.
	c.Language = func() string { return "fr" }
	full, err := c.Title(ctx, "movie", 550)
	if err != nil {
		t.Fatal(err)
	}
	if art := full.Artwork; art.Poster != "/posterfr12.jpg" || art.Backdrop != "/backdrop12.jpg" || art.Logo != "/logofr1234.png" {
		t.Errorf("artwork: %+v", art)
	}
	// Its other names, each once, in plain text; its own language.
	if full.OriginalName != "Fight Club" || full.Language != "en" || !slices.Equal(full.Names, []string{"El club de la lucha", "Club de combat"}) {
		t.Errorf("names: %q %q %q", full.OriginalName, full.Language, full.Names)
	}
	if full.Name != "Fight Club" || full.Year != 1999 || full.Runtime != 139 || full.Tagline != "Mischief." ||
		full.ReleaseDate != "1999-10-15" || full.Rating != 8.4 ||
		full.Overview != "An insomniac." || len(full.Cast) != 1 || full.Cast[0].Profile != "/norton1234.jpg" || full.Cast[0].ID != 819 {
		t.Errorf("title: %+v", full)
	}
	// A series' cast is the one over all its seasons.
	if s, err := c.Title(ctx, "series", 1400); err != nil || len(s.Cast) != 2 || s.Cast[0].Character != "Jerry" ||
		s.Cast[0].Profile != "/jerry12345.jpg" || s.Cast[1].Character != "" || s.Language != "" ||
		!slices.Equal(s.Names, []string{"Les Seinfeld"}) {
		t.Errorf("series cast: %+v %v", s, err)
	}
	person, err := c.Person(ctx, 1245)
	if err != nil || person.Name != "Scarlett Johansson" || person.Birthday != "1984-11-22" || person.Profile != "/scarlett123.jpg" ||
		len(person.Credits) != 3 || person.Credits[0].Title.Kind != "movie" || person.Credits[0].Role != "Natasha" ||
		person.Credits[1].Title.Kind != "series" || !person.Credits[2].Crew || person.Credits[2].Role != "Director" {
		t.Errorf("person: %+v %v", person, err)
	}
	titles, people, err := c.Search(ctx, "evasion")
	if err != nil || len(titles) != 2 || titles[0].Name != "Évasion" || titles[1].Kind != "series" ||
		len(people) != 1 || people[0].Profile != "/sly1234567.jpg" {
		t.Errorf("search: %+v %+v %v", titles, people, err)
	}
	if id, err := c.Find(ctx, "series", "imdb", "tt0944947"); err != nil || id != 1399 {
		t.Errorf("find a series: %d %v", id, err)
	}
	if _, err := c.Find(ctx, "movie", "imdb", "tt0944947"); !errors.Is(err, ErrNotFound) {
		t.Errorf("not a film: %v", err)
	}
	if _, err := c.Find(ctx, "movie", "imdb", "../x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad ID: %v", err)
	}
	season, err := c.Season(ctx, 1399, 1)
	if err != nil || season.Poster != "/season1abc.jpg" || season.Name != "Saison 1" || season.AirDate != "2011-04-17" || len(season.Episodes) != 3 {
		t.Fatalf("season: %+v %v", season, err)
	}
	// Plain text, checked dates and stills, each episode once.
	if e := season.Episodes[0]; e.Name != "Winter is coming" || e.Runtime != 62 || e.Still != "/still1abcd.jpg" {
		t.Errorf("episode 1: %+v", e)
	}
	if e := season.Episodes[1]; e.AirDate != "" || e.Still != "" {
		t.Errorf("episode 2: %+v", e)
	}
	if e := season.Episodes[2]; e.Still != "" || e.Number != 3 {
		t.Errorf("episode 3: %+v", e)
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
