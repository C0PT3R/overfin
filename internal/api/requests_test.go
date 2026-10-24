package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/overfin/server/internal/connector"
	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
)

// fakeSeerrRequests is Seerr for requests: users, requests and their
// statuses, as Seerr keeps them.
type fakeSeerrRequests struct {
	mu       sync.Mutex
	users    map[int]map[string]any
	requests map[int]map[string]any
	next     int
}

func (f *fakeSeerrRequests) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v1/user":
		f.next++
		f.users[f.next] = body
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"id":%d}`, f.next)
	case strings.HasPrefix(r.URL.Path, "/api/v1/user/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/user/"), &id)
		user := f.users[id]
		switch {
		case user == nil:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/settings/main"):
			json.NewEncoder(w).Encode(map[string]any{"username": user["username"], "email": user["email"], "locale": "fr"})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/settings/main"):
			if body["email"] != user["email"] || body["locale"] != "fr" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			user["username"] = body["username"]
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": id, "username": user["username"]})
		}
	case r.Method == "POST" && r.URL.Path == "/api/v1/request":
		// As Seerr does: the requester is the user the header names; the
		// API key alone would be its admin.
		var as float64
		fmt.Sscan(r.Header.Get("X-Api-User"), &as)
		if as == 0 || body["userId"] != nil {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body["userId"] = as
		for _, x := range f.requests {
			if x["mediaId"] == body["mediaId"] && x["userId"] == body["userId"] {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		f.next++
		body["status"] = 1
		f.requests[f.next] = body
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": f.next, "status": 1, "type": body["mediaType"],
			"media": map[string]any{"tmdbId": body["mediaId"], "status": 2}})
	case r.Method == "GET" && r.URL.Path == "/api/v1/request":
		var out []map[string]any
		for id, x := range f.requests {
			if by := r.URL.Query().Get("requestedBy"); by == "" || fmt.Sprint(x["userId"]) == by {
				out = append(out, map[string]any{"id": id, "status": x["status"], "type": x["mediaType"],
					"media": map[string]any{"tmdbId": x["mediaId"], "status": 2}})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/request/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/request/"), &id)
		if x := f.requests[id]; x != nil && fmt.Sprint(x["userId"]) != r.Header.Get("X-Api-User") {
			w.WriteHeader(http.StatusForbidden) // someone else's
			return
		}
		delete(f.requests, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNotFound)
	}
}

type fakeDirectory struct {
	people map[string]connector.Person
	err    error
}

func (f *fakeDirectory) People(context.Context) (map[string]connector.Person, error) {
	return f.people, f.err
}

func TestRequests(t *testing.T) {
	fake := &fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{}}
	seerrSrv := httptest.NewServer(fake)
	defer seerrSrv.Close()
	client, _ := seerr.New(seerrSrv.URL, "key")
	var st *store.Store
	dir := &fakeDirectory{people: map[string]connector.Person{subject: {Username: "alice", Name: "Alice Tremblay"}}}
	srv, _ := newServerWith(t, options{seerr: client, store: &st, directory: dir})
	ctx := context.Background()
	det, _ := json.Marshal(store.TitleDetails{Seasons: []store.SeasonInfo{{Number: 1, EpisodeCount: 2}, {Number: 2, EpisodeCount: 1}}})
	st.SetTitleDetails(ctx, store.Title{Kind: "series", TMDB: "1399", Name: "Le Trône de fer"}, det, time.Now())
	st.SetSeason(ctx, "1399", store.Season{Number: 1, Name: "Saison 1", AirDate: "2011-04-17", Episodes: []store.Episode{{Number: 1, AirDate: "2011-04-17"}}}, time.Now())
	st.SetSeason(ctx, "1399", store.Season{Number: 2, Name: "Saison 2", AirDate: "2012-04-01", Episodes: []store.Episode{{Number: 1, AirDate: "2012-04-01"}}}, time.Now())
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "series", TMDB: "1399"}, nil, "2011-04-17", "2011-04-17", time.Now())
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "100"}, nil, "2024-01-01", "2024-03-01", time.Now())
	film, _ := json.Marshal(store.TitleDetails{})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Pas ici"}, film, time.Now())

	type request struct {
		ID, Status string
		Seasons    []int
		Title      struct {
			ID      string
			Request *struct{ ID, Status string }
		}
	}
	// A season of a series no library has: the account's Seerr user is
	// made first, under the account's name, with a placeholder address and
	// a password Seerr never mails.
	code, body := post(t, srv, "/api/v1/requests", `{"titleId":"title_series_tmdb_1399_s2"}`)
	var made request
	json.Unmarshal([]byte(body), &made)
	if code != 201 || made.Status != "pending" || made.Title.ID != "title_series_tmdb_1399" || len(made.Seasons) != 1 || made.Seasons[0] != 2 {
		t.Fatalf("request a season: %d %s", code, body)
	}
	if len(fake.users) != 1 || fake.users[1]["username"] != "Alice Tremblay" ||
		!strings.HasSuffix(fmt.Sprint(fake.users[1]["email"]), "@overfin.invalid") ||
		fake.users[1]["password"] == "" || strings.Contains(fmt.Sprint(fake.users[1]), subject) {
		t.Errorf("Seerr user: %v", fake.users)
	}
	// The season, and its episodes, say they're requested; the first
	// season doesn't.
	var s2, s1 struct{ Request *struct{ ID, Status string } }
	get(t, srv, "/api/v1/titles/title_series_tmdb_1399_s2", &s2)
	get(t, srv, "/api/v1/titles/title_series_tmdb_1399_s1", &s1)
	if s2.Request == nil || s2.Request.ID != made.ID || s2.Request.Status != "pending" || s1.Request != nil {
		t.Errorf("titles' requests: %+v %+v", s2.Request, s1.Request)
	}

	// A film; asked twice, refused the second time. Not a title: not found.
	if code, _ := post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_100"}`); code != 201 {
		t.Errorf("request a film: %d", code)
	}
	if code, body := post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_100"}`); code != 409 || !strings.Contains(body, "already-requested") {
		t.Errorf("twice: %d %s", code, body)
	}
	if code, _ := post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_555"}`); code != 404 {
		t.Errorf("unknown title: %d", code)
	}
	if len(fake.users) != 1 {
		t.Errorf("one Seerr user per account: %d", len(fake.users))
	}
	// A new name at the identity provider: the Seerr user is renamed at the
	// next request; not while the provider can't be asked.
	dir.err = io.ErrUnexpectedEOF
	post(t, srv, "/api/v1/requests", `{"titleId":"title_series_tmdb_1399_s1"}`)
	if fake.users[1]["username"] != "Alice Tremblay" {
		t.Errorf("renamed on a guess: %v", fake.users[1])
	}
	dir.err, dir.people[subject] = nil, connector.Person{Username: "alice", Name: "Alice Roy"}
	post(t, srv, "/api/v1/requests", `{"titleId":"title_series_tmdb_1399_s1"}`)
	if fake.users[1]["username"] != "Alice Roy" || len(fake.users) != 1 {
		t.Errorf("renamed: %v", fake.users)
	}

	// Seerr approves the season: the list says so.
	fake.mu.Lock()
	fake.requests[2]["status"] = 2
	fake.mu.Unlock()
	var list struct{ Items []request }
	if code := get(t, srv, "/api/v1/requests", &list); code != 200 || len(list.Items) != 2 {
		t.Fatalf("list: %d %+v", code, list)
	}
	byTitle := map[string]request{}
	for _, x := range list.Items {
		byTitle[x.Title.ID] = x
	}
	if byTitle["title_series_tmdb_1399"].Status != "approved" || byTitle["title_movie_tmdb_100"].Status != "pending" {
		t.Errorf("statuses: %+v", list.Items)
	}

	// Cancelling: a pending one only.
	if code, _ := put(t, srv, "DELETE", "/api/v1/requests/"+made.ID, ""); code != 409 {
		t.Errorf("cancel an approved one: %d", code)
	}
	film100 := byTitle["title_movie_tmdb_100"].ID
	if code, _ := put(t, srv, "DELETE", "/api/v1/requests/"+film100, ""); code != 204 || len(fake.requests) != 1 {
		t.Errorf("cancel: %d %v", code, fake.requests)
	}

	// Another profile of the account sees none of them; requests are each
	// profile's.
	_, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	post(t, srv, "/api/v1/profiles/"+between(body, `"id":"`, `"`)+"/activate", `{}`)
	list.Items = nil
	if get(t, srv, "/api/v1/requests", &list); len(list.Items) != 0 {
		t.Errorf("another profile's list: %+v", list.Items)
	}
}

// A title someone else requested, in Seerr itself: it says it's
// requested, but not by whom, and isn't the account's to cancel.
func TestRequestedElsewhere(t *testing.T) {
	fake := &fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{
		7: {"mediaId": float64(100), "mediaType": "movie", "userId": float64(99), "status": 1},
	}, next: 7}
	seerrSrv := httptest.NewServer(fake)
	defer seerrSrv.Close()
	client, _ := seerr.New(seerrSrv.URL, "key")
	var st *store.Store
	srv, _ := newServerWith(t, options{seerr: client, store: &st})
	ctx := context.Background()
	film, _ := json.Marshal(store.TitleDetails{})
	st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: "100", Name: "Pas ici"}, film, time.Now())
	st.SetTitleRatings(ctx, store.RatedTitle{Kind: "movie", TMDB: "100"}, nil, "2024-01-01", "2024-03-01", time.Now())

	var title struct {
		Request *struct{ ID, Status string }
	}
	for i := 0; i < 100 && title.Request == nil; i++ {
		get(t, srv, "/api/v1/titles/title_movie_tmdb_100", &title) // read in the background
		time.Sleep(10 * time.Millisecond)
	}
	if title.Request == nil || title.Request.Status != "pending" || title.Request.ID != "" {
		t.Fatalf("requested elsewhere: %+v", title.Request)
	}
}

// Without Seerr, there are no requests.
func TestNoRequestsWithoutSeerr(t *testing.T) {
	srv, _ := newServer(t)
	if code := get(t, srv, "/api/v1/requests", nil); code != 404 {
		t.Errorf("list: %d", code)
	}
	if code, _ := post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_100"}`); code != 404 {
		t.Errorf("request: %d", code)
	}
}

// Requests to Seerr are limited per account (rule 31).
func TestRequestsLimited(t *testing.T) {
	seerrSrv := httptest.NewServer(&fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{}})
	defer seerrSrv.Close()
	client, _ := seerr.New(seerrSrv.URL, "key")
	srv, _ := newServerWith(t, options{seerr: client})
	code := 0
	for i := 0; i < 21 && code != 429; i++ {
		code, _ = post(t, srv, "/api/v1/requests", `{"titleId":"title_movie_tmdb_999999"}`)
	}
	if code != 429 {
		t.Errorf("21 requests in a minute: %d", code)
	}
}
