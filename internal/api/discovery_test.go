package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/C0PT3R/overfin-server/internal/catalog"
	"github.com/C0PT3R/overfin-server/internal/seerr"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// fakeSeerr answers discovery: two films trending (949, in the library;
// 100, missing) and a series mixed in, and their details.
func fakeSeerr(t *testing.T) *seerr.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "seerr-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		film := func(id int, poster string) map[string]any {
			return map[string]any{"id": id, "mediaType": "movie", "title": fmt.Sprintf("Film %d <i>!</i>", id),
				"releaseDate": "1995-12-15", "overview": "A film.", "posterPath": poster}
		}
		switch r.URL.Path {
		case "/api/v1/discover/trending":
			json.NewEncoder(w).Encode(map[string]any{"page": 1, "totalPages": 1, "results": []any{
				film(949, "/poster949abcdef.jpg"), film(100, "../../etc/passwd"),
				map[string]any{"id": 1399, "mediaType": "tv", "name": "A series"},
			}})
		case "/api/v1/movie/949", "/api/v1/movie/555":
			id := 949
			if strings.HasSuffix(r.URL.Path, "555") {
				id = 555 // in no list: opened from a link
			}
			d := film(id, "/poster949abcdef.jpg")
			d["genres"] = []any{map[string]any{"name": "Crime"}}
			d["credits"] = map[string]any{"cast": []any{map[string]any{"name": "Al Pacino", "character": "Hanna"}}}
			json.NewEncoder(w).Encode(d)
		default:
			if strings.HasPrefix(r.URL.Path, "/api/v1/discover/") {
				json.NewEncoder(w).Encode(map[string]any{"page": 1, "totalPages": 1, "results": []any{}})
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := seerr.New(srv.URL, "seerr-key")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestDiscover(t *testing.T) {
	var st *store.Store
	s := fakeSeerr(t)
	srv, _ := newServerWith(t, options{seerr: s, store: &st})
	// The library has film 949: item 3 (which the fake Jellyfin serves).
	if err := st.PutCatalog(context.Background(), []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
	}, 1); err != nil {
		t.Fatal(err)
	}
	// The lists, read in the background as Overfin does.
	lists := catalog.NewLists(func() *seerr.Client { return s }, st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Hour, 1)
	if err := lists.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	type title struct {
		TMDBID, MediaKind, Name, Availability string
		Images                                *struct{ Primary *struct{ URL string } }
		LibraryItem                           *struct{ ID string }
	}
	var out struct {
		Items      []title
		NextCursor string
	}
	if code := get(t, srv, "/api/v1/discover?list=trending&mediaKind=movie", &out); code != 200 {
		t.Fatalf("discover: %d", code)
	}
	if len(out.Items) != 2 || out.NextCursor != "" {
		t.Fatalf("items: %+v", out)
	}
	have, missing := out.Items[0], out.Items[1]
	if have.TMDBID != "949" || have.Availability != "available" || have.LibraryItem == nil ||
		have.LibraryItem.ID != fmt.Sprintf("item_%032x", 3) || strings.Contains(have.Name, "<") {
		t.Errorf("in the library: %+v", have)
	}
	if missing.Availability != "missing" || missing.LibraryItem != nil || missing.Images != nil {
		t.Errorf("missing (and its unsafe poster dropped): %+v", missing)
	}

	// Filters.
	for avail, want := range map[string]string{"library": "949", "missing": "100"} {
		out.Items = nil
		get(t, srv, "/api/v1/discover?list=trending&mediaKind=movie&availability="+avail, &out)
		if len(out.Items) != 1 || out.Items[0].TMDBID != want {
			t.Errorf("%s: %+v", avail, out.Items)
		}
	}
	for _, bad := range []string{"list=nope&mediaKind=movie", "list=trending&mediaKind=book",
		"list=trending&mediaKind=movie&availability=x", "list=trending&mediaKind=movie&extra=1"} {
		if code := get(t, srv, "/api/v1/discover?"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}

	// A title's page.
	var det struct {
		title
		Genres []string
		Cast   []struct{ Name, Character string }
	}
	if code := get(t, srv, "/api/v1/catalog/movie/949", &det); code != 200 || det.Availability != "available" ||
		len(det.Genres) != 1 || len(det.Cast) != 1 || det.Cast[0].Name != "Al Pacino" {
		t.Errorf("title: %d %+v", code, det)
	}
	if code := get(t, srv, "/api/v1/catalog/movie/12345", nil); code != 404 {
		t.Errorf("unknown title: %d", code)
	}
	// A title in no list, opened from a link: asked of Seerr, then kept.
	if code := get(t, srv, "/api/v1/catalog/movie/555", nil); code != 200 {
		t.Errorf("a linked title: %d", code)
	}
	if kept, _ := st.TitleByTMDB(context.Background(), "movie", "555"); kept == nil || kept.Details == nil {
		t.Errorf("not kept: %+v", kept)
	}

	// An image ID with a path Seerr never gave is refused before TMDB is
	// asked for anything.
	if code := get(t, srv, "/api/v1/images/tmdb_movie_949_poster_inventedpath1_jpg", nil); code != 404 {
		t.Errorf("invented image path: %d", code)
	}

	// A kids profile sees none of it.
	_, body := post(t, srv, "/api/v1/profiles", `{"name":"Kid","kind":"kids","ratingCeiling":"G"}`)
	var kid struct{ ID string }
	json.Unmarshal([]byte(body), &kid)
	if code, body := post(t, srv, "/api/v1/profiles/"+kid.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate kid: %d %s", code, body)
	}
	out.Items = nil
	if code := get(t, srv, "/api/v1/discover?list=trending&mediaKind=movie", &out); code != 200 || len(out.Items) != 0 {
		t.Errorf("kids discover: %d %+v", code, out.Items)
	}
	if code := get(t, srv, "/api/v1/catalog/movie/949", nil); code != 404 {
		t.Errorf("kids title: %d", code)
	}
	if code := get(t, srv, "/api/v1/images/tmdb_movie_949_poster_poster949abcdef_jpg", nil); code != 404 {
		t.Errorf("kids image: %d", code)
	}
}
