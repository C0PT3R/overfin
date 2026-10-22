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
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/user/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/user/"), &id)
		if f.users[id] == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"id":%d}`, id)
	case r.Method == "POST" && r.URL.Path == "/api/v1/request":
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
			if fmt.Sprint(x["userId"]) == r.URL.Query().Get("requestedBy") {
				out = append(out, map[string]any{"id": id, "status": x["status"], "type": x["mediaType"],
					"media": map[string]any{"tmdbId": x["mediaId"], "status": 2}})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/request/"):
		var id int
		fmt.Sscan(strings.TrimPrefix(r.URL.Path, "/api/v1/request/"), &id)
		delete(f.requests, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestRequests(t *testing.T) {
	fake := &fakeSeerrRequests{users: map[int]map[string]any{}, requests: map[int]map[string]any{}}
	seerrSrv := httptest.NewServer(fake)
	defer seerrSrv.Close()
	client, _ := seerr.New(seerrSrv.URL, "key")
	var st *store.Store
	srv, _ := newServerWith(t, options{seerr: client, store: &st})
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
	// made first, anonymous, with a password Seerr never mails.
	code, body := post(t, srv, "/api/v1/requests", `{"titleId":"title_series_tmdb_1399_s2"}`)
	var made request
	json.Unmarshal([]byte(body), &made)
	if code != 201 || made.Status != "pending" || made.Title.ID != "title_series_tmdb_1399" || len(made.Seasons) != 1 || made.Seasons[0] != 2 {
		t.Fatalf("request a season: %d %s", code, body)
	}
	if len(fake.users) != 1 || !strings.HasSuffix(fmt.Sprint(fake.users[1]["email"]), "@overfin.invalid") ||
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
