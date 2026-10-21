package api_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The watchlist module, end to end through the core: its routes, the
// calling profile, the backend as that profile, its own table.
func TestWatchlistModule(t *testing.T) {
	srv, _ := newServer(t)
	movie1, movie3 := "item_"+fmt.Sprintf("%032x", 1), "item_"+fmt.Sprintf("%032x", 3)

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
	// Only titles, and only well-formed IDs: a person isn't a title.
	if code, _ := put(t, srv, "PUT", "/api/v1/me/watchlist/item_"+fmt.Sprintf("%032x", 99), ""); code != 404 {
		t.Errorf("a person on the watchlist: %d", code)
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
	srv, _ := newServerWith(t, options{provider: idp.provider(), dbs: &dbs})
	movie1 := "item_" + fmt.Sprintf("%032x", 1)
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
