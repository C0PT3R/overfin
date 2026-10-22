package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakePeople is TMDB for people's pages.
type fakePeople struct{ asked int }

func (f *fakePeople) Title(context.Context, string, int) (*tmdb.Full, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakePeople) Season(context.Context, int, int) (*tmdb.SeasonFull, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakePeople) Find(context.Context, string, string, string) (int, error) {
	return 0, tmdb.ErrNotFound
}
func (f *fakePeople) Person(_ context.Context, id int) (*tmdb.PersonFull, error) {
	if id != 1245 {
		return nil, tmdb.ErrNotFound
	}
	f.asked++
	film := func(n int, name string, year int) tmdb.Title {
		return tmdb.Title{TMDBID: n, Kind: "movie", Name: name, Year: year}
	}
	return &tmdb.PersonFull{ID: 1245, Name: "Scarlett Johansson", Birthday: "1984-11-22", Profile: "/scarlett123.jpg",
		Credits: []tmdb.Credit{
			{Title: film(10, "Not here", 2020), Role: "Natasha"},
			{Title: film(949, "Here", 1995), Role: "Lady"},
			{Title: film(949, "Here", 1995), Role: "Director", Crew: true},
			{Title: film(11, "Coming", 2099), Role: "Someone"},
		}}, nil
}

// A person's page: fetched from TMDB when opened, their titles those a
// library has first, each once with every role, and only those out.
func TestPerson(t *testing.T) {
	var st *store.Store
	src := &fakePeople{}
	srv, _ := newServerWith(t, options{store: &st, tmdb: src})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Here", SortName: "h", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	for n, dates := range map[string][2]string{"10": {"2020-01-01", "2020-05-01"}, "11": {"2099-01-01", ""}} {
		st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: n}, nil, dates[0], dates[1], time.Now())
	}

	var p struct {
		ID, Name, Birthday string
		Image              *struct{ URL string }
		Credits            []struct {
			Title struct{ ID, Availability string }
			Roles []string
		}
	}
	code, body := getAs(t, srv, subject, "/api/v1/people/person_tmdb_1245")
	json.Unmarshal([]byte(body), &p)
	if code != 200 || p.Name != "Scarlett Johansson" || p.Image == nil || p.Image.URL != "/api/v1/images/tmdbart_profile_scarlett123_jpg" {
		t.Fatalf("person: %d %s", code, body)
	}
	if len(p.Credits) != 2 || p.Credits[0].Title.ID != "title_movie_tmdb_949" || len(p.Credits[0].Roles) != 2 ||
		p.Credits[1].Title.ID != "title_movie_tmdb_10" || p.Credits[1].Title.Availability != "missing" {
		t.Errorf("credits: %+v", p.Credits)
	}
	// Kept: not asked again within the week. Their photo is served.
	getAs(t, srv, subject, "/api/v1/people/person_tmdb_1245")
	if src.asked != 1 {
		t.Errorf("asked TMDB %d times", src.asked)
	}
	for _, bad := range []string{"person_tmdb_999", "person_tmdb_01245", "person_1245", "title_movie_tmdb_949"} {
		if code, _ := getAs(t, srv, subject, "/api/v1/people/"+bad); code != 404 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}
