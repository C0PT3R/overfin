package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/C0PT3R/overfin/internal/api"
	"github.com/C0PT3R/overfin/internal/identity"
	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/provision"
	"github.com/C0PT3R/overfin/internal/seal"
	"github.com/C0PT3R/overfin/internal/server"
	"github.com/C0PT3R/overfin/internal/store"
)

const (
	userJF  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token   = "profile-token"
	subject = "test-subject"
)

// fakeJellyfin serves 7 movies and checks every call uses the profile's token.
type fakeJellyfin struct {
	mu       sync.Mutex
	badAuth  int
	lastQury url.Values
}

func movie(i int) map[string]any {
	return map[string]any{"Id": fmt.Sprintf("%032x", i+1), "Name": fmt.Sprintf("Movie %d", i), "Type": "Movie"}
}

func (f *fakeJellyfin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(r.Header.Get("Authorization"), `Token="`+token+`"`) {
		f.badAuth++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	f.lastQury = q
	if q.Get("userId") != userJF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.URL.Path == "/Items":
		start, _ := strconv.Atoi(q.Get("startIndex"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		var items []map[string]any
		for i := start; i < 7 && i < start+limit; i++ {
			items = append(items, movie(i))
		}
		json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 7})
	case r.URL.Path == "/Items/"+fmt.Sprintf("%032x", 1):
		json.NewEncoder(w).Encode(movie(0))
	case strings.HasPrefix(r.URL.Path, "/Items/"):
		w.WriteHeader(http.StatusNotFound) // not visible to this user
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newServer(t *testing.T) (*httptest.Server, *fakeJellyfin) {
	t.Helper()
	fake := &fakeJellyfin{}
	jfSrv := httptest.NewServer(fake)
	t.Cleanup(jfSrv.Close)
	jf, _ := jellyfin.New(jfSrv.URL, "test")

	box, _ := seal.New(bytes.Repeat([]byte{1}, 32))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, _, err := st.CreateAccount(context.Background(), subject, store.NewOwner{
		Name: "Main", JellyfinUserID: userJF, JellyfinUsername: "of-test", JellyfinToken: token,
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	prov := &provision.Provisioner{Store: st, Jellyfin: jf, LogKey: []byte("k"), Logger: logger}
	ids := identity.NewForwardAuth([]netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, "Remote-User", "")
	h, err := server.New(logger, ids, api.Library{Provisioner: prov, Jellyfin: jf, CursorKey: []byte("cursor-key"), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, fake
}

func get(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Remote-User", subject)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

type page struct {
	Items      []struct{ ID, Kind, Name string }
	NextCursor string
	TotalCount int
	Type       string // set on errors
}

func TestPagingWithSignedCursors(t *testing.T) {
	srv, fake := newServer(t)

	var p1 page
	if code := get(t, srv, "/api/v1/items?kind=movie&limit=3", &p1); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(p1.Items) != 3 || p1.TotalCount != 7 || p1.NextCursor == "" || p1.Items[0].Kind != "movie" {
		t.Fatalf("first page: %+v", p1)
	}
	if fake.lastQury.Get("includeItemTypes") != "Movie" || fake.lastQury.Get("recursive") != "true" {
		t.Errorf("Jellyfin query: %v", fake.lastQury)
	}

	var p2 page
	get(t, srv, "/api/v1/items?kind=movie&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &p2)
	if len(p2.Items) != 3 || p2.Items[0].Name != "Movie 3" {
		t.Errorf("second page: %+v", p2)
	}

	// The same cursor with other filters is refused.
	var bad page
	code := get(t, srv, "/api/v1/items?kind=series&limit=3&cursor="+url.QueryEscape(p1.NextCursor), &bad)
	if code != http.StatusBadRequest || bad.Type != "invalid-cursor" {
		t.Errorf("reused cursor: %d %q", code, bad.Type)
	}
	if fake.badAuth != 0 {
		t.Errorf("%d Jellyfin calls without the profile's token", fake.badAuth)
	}
}

func TestInvalidQueries(t *testing.T) {
	srv, _ := newServer(t)
	for _, q := range []string{
		"?kind=folder",
		"?sort=random",
		"?limit=0",
		"?limit=500",
		"?library=../../x",
		"?genre=a|b",
		"?yearFrom=12",
		"?unknown=1",
		"?parent=item_ZZZ",
	} {
		var p page
		if code := get(t, srv, "/api/v1/items"+q, &p); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", q, code)
		}
	}
}

func TestItemDetailAndHiddenItems(t *testing.T) {
	srv, _ := newServer(t)
	var it struct{ ID, Name string }
	if code := get(t, srv, "/api/v1/items/item_"+fmt.Sprintf("%032x", 1), &it); code != 200 || it.Name != "Movie 0" {
		t.Errorf("visible item: %d %+v", code, it)
	}
	// Jellyfin says 404 for items the profile may not see; so does Overfin.
	if code := get(t, srv, "/api/v1/items/item_"+fmt.Sprintf("%032x", 99), nil); code != http.StatusNotFound {
		t.Errorf("hidden item: status %d", code)
	}
	if code := get(t, srv, "/api/v1/items/not-an-id", nil); code != http.StatusNotFound {
		t.Errorf("malformed ID: status %d", code)
	}
}

func TestNoAccountMeansSetupRequired(t *testing.T) {
	srv, _ := newServer(t)
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/libraries", nil)
	req.Header.Set("Remote-User", "someone-new")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var p page
	json.NewDecoder(res.Body).Decode(&p)
	res.Body.Close()
	if p.Type != "account-setup-required" {
		t.Errorf("type = %q", p.Type)
	}
}
