package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// Identification: the library's titles, suspects first and why; TMDB
// searched by kind; a title fixed, confirmed, given back to Jellyfin.
func TestIdentification(t *testing.T) {
	ctx := context.Background()
	p, b, db, _ := signedInPanel(t)
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	db.PutCatalog(ctx, []store.CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "949", FolderName: "Heat (1995)"},
		{ID: id(1), Kind: "movie", Name: "L'Odyssée", SortName: "b", TMDB: "1368337", FolderName: "The Swedish Connection (2026)"},
	}, 1)
	db.RefreshTitleKeys(ctx)
	db.PutTitles(ctx, []store.Title{{Kind: "movie", TMDB: "949", Name: "Heat", Year: 1995},
		{Kind: "movie", TMDB: "1368337", Name: "L'Odyssée", Year: 2026}}, time.Now())
	retitled := 0
	p.OnRetitled = func() { retitled++ }
	p.SearchTMDB = func(_ context.Context, q string) ([]tmdb.Title, error) {
		return []tmdb.Title{{TMDBID: 1261188, Kind: "movie", Name: "The Swedish Connection", Year: 2026, Language: "sv", Poster: "/swedish123.jpg"},
			{TMDBID: 5, Kind: "series", Name: "A series"}}, nil
	}

	var list struct {
		Items []struct {
			ItemID, TMDB string
			Overridden   bool
			Reasons      []struct{ Code string }
		}
		Suspects int
	}
	json.Unmarshal(b.do("GET", "/admin/api/identification", "", nil).Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Suspects != 1 || list.Items[0].ItemID != id(1) || list.Items[0].Reasons[0].Code != "name" ||
		len(list.Items[1].Reasons) != 0 {
		t.Fatalf("list: %+v", list)
	}

	rec := b.do("POST", "/admin/api/identification/search", `{"query":"swedish connection","kind":"movie"}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tmdb":"1261188"`) || strings.Contains(rec.Body.String(), "A series") ||
		!strings.Contains(rec.Body.String(), `"poster":"/admin/api/posters/tmdbart_poster_swedish123_jpg"`) {
		t.Errorf("search: %d %s", rec.Code, rec.Body)
	}

	if rec := b.do("PUT", "/admin/api/identification/"+id(1), `{"tmdb":"1261188"}`, nil); rec.Code != 204 || retitled != 1 {
		t.Fatalf("identify: %d %s", rec.Code, rec.Body)
	}
	json.Unmarshal(b.do("GET", "/admin/api/identification", "", nil).Body.Bytes(), &list)
	if list.Suspects != 0 {
		t.Errorf("after the fix: %+v", list)
	}
	for _, it := range list.Items {
		if it.ItemID == id(1) && (it.TMDB != "1261188" || !it.Overridden) {
			t.Errorf("fixed: %+v", it)
		}
	}
	if rec := b.do("DELETE", "/admin/api/identification/"+id(1), "", nil); rec.Code != 204 {
		t.Errorf("undo: %d", rec.Code)
	}
	if o, _ := db.Overrides(ctx); len(o) != 0 {
		t.Errorf("overrides left: %v", o)
	}
	for _, bad := range []struct{ path, body string }{
		{"/admin/api/identification/" + id(1), `{"tmdb":"0"}`},
		{"/admin/api/identification/" + id(1), `{"tmdb":"12a"}`},
		{"/admin/api/identification/" + id(1), `{"tmdb":"012"}`},
		{"/admin/api/identification/not-an-id", `{"tmdb":"12"}`},
	} {
		if rec := b.do("PUT", bad.path, bad.body, nil); rec.Code != 400 {
			t.Errorf("%s %s: %d", bad.path, bad.body, rec.Code)
		}
	}
	if rec := b.do("PUT", "/admin/api/identification/"+strings.Repeat("f", 32), `{"tmdb":"12"}`, nil); rec.Code != 404 {
		t.Errorf("unknown item: %d", rec.Code)
	}
}
