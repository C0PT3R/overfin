package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// watchlistTitles gives the test store two films: 1 in the library (item
// 1), 3 not.
func watchlistTitles(st *store.Store) {
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{{ID: fmt.Sprintf("%032x", 1), Kind: "movie", Name: "One", SortName: "one", TMDB: "1"}}, 1)
	st.RefreshTitleKeys(ctx)
	det, _ := json.Marshal(store.TitleDetails{})
	for _, n := range []string{"1", "3"} {
		st.SetTitleDetails(ctx, store.Title{Kind: "movie", TMDB: n, Name: "Film " + n}, det, time.Now())
	}
}

// The watchlist module, end to end through the core: its routes, the
// calling profile, the backend as that profile, its own table. Titles go
// on it whether a library has them or not.
func TestWatchlistModule(t *testing.T) {
	var st *store.Store
	var dbs map[string]*sql.DB
	srv, _ := newServerWith(t, options{store: &st, dbs: &dbs})
	watchlistTitles(st)
	movie1, movie3 := "title_movie_tmdb_1", "title_movie_tmdb_3"

	for _, id := range []string{movie1, movie3} {
		if code, body := put(t, srv, "PUT", "/api/v1/me/watchlist/"+id, ""); code != 204 {
			t.Fatalf("add %s: %d %s", id, code, body)
		}
	}
	code, body := getAs(t, srv, subject, "/api/v1/me/watchlist")
	if code != 200 || !strings.Contains(body, movie1) || strings.Index(body, movie3) > strings.Index(body, movie1) {
		t.Fatalf("list, newest first: %d %s", code, body)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/me/watchlist/"+movie1); code != 204 {
		t.Errorf("on the list: %d", code)
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/me/watchlist/"+movie1, ""); code != 204 {
		t.Errorf("remove: %d", code)
	}
	if code, _ := getAs(t, srv, subject, "/api/v1/me/watchlist/"+movie1); code != 404 {
		t.Errorf("after removing: %d", code)
	}
	// Only titles that exist, and only well-formed IDs.
	if code, _ := put(t, srv, "PUT", "/api/v1/me/watchlist/title_movie_tmdb_99", ""); code != 404 {
		t.Errorf("an unknown title on the watchlist: %d", code)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/me/watchlist/item_"+fmt.Sprintf("%032x", 1), ""); code != 404 {
		t.Errorf("a library item on the watchlist: %d", code)
	}
	// An entry kept before titles (a library item) becomes its title.
	if _, err := dbs["watchlist"].Exec(`INSERT INTO watchlist (profile_id, item_id, added_at) VALUES (?, ?, 1)`,
		ownerID(t, srv), "item_"+fmt.Sprintf("%032x", 1)); err != nil {
		t.Fatal(err)
	}
	if code, body := getAs(t, srv, subject, "/api/v1/me/watchlist"); code != 200 || !strings.Contains(body, movie1) || strings.Contains(body, "item_") {
		t.Errorf("an old entry: %d %s", code, body)
	}
	if code, _ := put(t, srv, "PUT", "/api/v1/me/watchlist/garbage", ""); code != 404 {
		t.Errorf("a malformed ID: %d", code)
	}
	// Private to the profile.
	_, body = post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	post(t, srv, "/api/v1/profiles/"+between(body, `"id":"`, `"`)+"/activate", `{}`)
	if code, body := getAs(t, srv, subject, "/api/v1/me/watchlist"); code != 200 || strings.Contains(body, movie3) {
		t.Errorf("another profile's watchlist: %d %s", code, body)
	}
	// From that profile (no PIN), its own export includes its (empty) list.
	if code, body := post(t, srv, "/api/v1/me/export", `{}`); code != 200 || !strings.Contains(body, `"watchlist":[]`) {
		t.Errorf("export with the module's part: %d %s", code, body)
	}
}

// A deleted account's watchlists go with it, from the watchlist's own
// database: the core tells the module.
func TestDeletedAccountsWatchlistIsForgotten(t *testing.T) {
	idp := newFakeIdP(t)
	var dbs map[string]*sql.DB
	var st *store.Store
	srv, _ := newServerWith(t, options{provider: idp.provider(), dbs: &dbs, store: &st})
	watchlistTitles(st)
	movie1 := "title_movie_tmdb_1"
	owner := ownerID(t, srv)
	if code, body := put(t, srv, "PUT", "/api/v1/me/watchlist/"+movie1, ""); code != 204 {
		t.Fatalf("watchlist: %d %s", code, body)
	}
	count := func() (n int) {
		dbs["watchlist"].QueryRow(`SELECT count(*) FROM watchlist WHERE profile_id = ?`, owner).Scan(&n)
		return n
	}
	if count() != 1 {
		t.Fatalf("watchlist rows before: %d", count())
	}
	stepUp(t, srv, idp, subject, time.Now())
	if code, body := put(t, srv, "DELETE", "/api/v1/me", ""); code != 200 {
		t.Fatalf("delete the account: %d %s", code, body)
	}
	if n := count(); n != 0 {
		t.Errorf("the deleted account's watchlist stayed: %d rows", n)
	}
}
