package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// fakePeople is TMDB for people's pages.
type fakePeople struct{ asked, searched int }

func (f *fakePeople) Title(context.Context, string, int) (*tmdb.Full, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakePeople) Season(context.Context, int, int) (*tmdb.SeasonFull, error) {
	return nil, tmdb.ErrNotFound
}
func (f *fakePeople) Find(context.Context, string, string, string) (int, error) {
	return 0, tmdb.ErrNotFound
}

// Release: film 20 is out at home, 21 only to come.
func (f *fakePeople) Release(_ context.Context, kind string, id int) (*tmdb.Release, error) {
	switch id {
	case 20:
		return &tmdb.Release{Released: "2013-10-18", AtHome: "2014-02-04"}, nil
	case 21:
		return &tmdb.Release{Released: "2099-01-01"}, nil
	}
	return nil, tmdb.ErrNotFound
}

func (f *fakePeople) Search(_ context.Context, q string) ([]tmdb.Title, []tmdb.PersonHit, error) {
	f.searched++
	return []tmdb.Title{
		{TMDBID: 20, Kind: "movie", Name: "Évasion 2", Year: 2013},
		{TMDBID: 949, Kind: "movie", Name: "Évasion", Year: 2013},
		{TMDBID: 21, Kind: "movie", Name: "Évasion 3", Year: 2099},
	}, []tmdb.PersonHit{{ID: 1245, Name: "Sly", Profile: "/sly1234567.jpg"}}, nil
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

// Search: the library's titles first (TMDB's names), then TMDB's others
// that are out, and people; the terms in the body.
func TestSearch(t *testing.T) {
	var st *store.Store
	src := &fakePeople{}
	srv, _ := newServerWith(t, options{store: &st, tmdb: src})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "Escape Plan", SortName: "e", TMDB: "949"}}, 1)
	st.RefreshTitleKeys(ctx)
	st.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "949", Name: "Évasion"}}, time.Now())

	var res struct {
		Titles []struct{ ID, Availability string }
		People []struct {
			ID    string
			Image *struct{ URL string }
		}
	}
	code, body := post(t, srv, "/api/v1/search", `{"query":"evasion"}`)
	json.Unmarshal([]byte(body), &res)
	if code != 200 || len(res.Titles) != 2 || res.Titles[0].ID != "title_movie_tmdb_949" || res.Titles[0].Availability != "available" ||
		res.Titles[1].ID != "title_movie_tmdb_20" {
		t.Fatalf("search: %d %s", code, body)
	}
	if len(res.People) != 1 || res.People[0].ID != "person_tmdb_1245" || res.People[0].Image == nil {
		t.Errorf("people: %+v", res.People)
	}
	// Too short: nothing, and TMDB isn't asked; too long: refused.
	searched := src.searched
	if code, body := post(t, srv, "/api/v1/search", `{"query":" e "}`); code != 200 || !strings.Contains(body, `"titles":[]`) || src.searched != searched {
		t.Errorf("too short: %d %s", code, body)
	}
	if code, _ := post(t, srv, "/api/v1/search", `{"query":"`+strings.Repeat("x", 201)+`"}`); code != 400 {
		t.Errorf("too long: %d", code)
	}
}
