package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// A profile limited to some libraries, with a tag hidden from it, sees
// only that, from the index and from Jellyfin alike.
func TestLibrariesAndHiddenTags(t *testing.T) {
	var st *store.Store
	srv, fake := newServerWith(t, options{store: &st})
	fake.partnerSeesAll = true // the second profile below
	ctx := context.Background()
	films, music := fmt.Sprintf("%032x", 0xf1), fmt.Sprintf("%032x", 0xf2)
	// Films 1 to 5 in the films library (5 tagged Horror), 6 in music; 7
	// isn't in the index at all (Jellyfin puts it in music).
	var items []store.CatalogItem
	for i := 1; i <= 6; i++ {
		it := store.CatalogItem{ID: fmt.Sprintf("%032x", i), Kind: "movie", Name: fmt.Sprint(i), SortName: fmt.Sprint(i), LibraryID: films}
		if i == 5 {
			it.Tags = []string{"Horror"}
		}
		if i == 6 {
			it.LibraryID = music
		}
		items = append(items, it)
	}
	if err := st.PutCatalog(ctx, items, 1); err != nil {
		t.Fatal(err)
	}
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})

	code, body := post(t, srv, "/api/v1/profiles", `{"name":"Partner"}`)
	var partner struct{ ID string }
	json.Unmarshal([]byte(body), &partner)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	for _, bad := range []string{`{"libraries":["lib_` + strings.Repeat("e", 32) + `"]}`, `{"blockedTags":[""]}`, `{"libraries":"x"}`} {
		if code, _ := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, body = put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":["lib_`+films+`"],"blockedTags":["horror"]}`)
	if code != 200 || !strings.Contains(body, `"libraries":["lib_`+films+`"]`) || !strings.Contains(body, `"blockedTags":["horror"]`) {
		t.Fatalf("limit: %d %s", code, body)
	}
	if code, body := getAs(t, srv, subject, "/api/v1/tags"); code != 200 || body != `{"items":["Horror"]}`+"\n" {
		t.Errorf("tags: %d %s", code, body)
	}
	if code, body := post(t, srv, "/api/v1/profiles/"+partner.ID+"/activate", `{}`); code != 200 {
		t.Fatalf("activate: %d %s", code, body)
	}

	ids := func(path string) ([]string, *int) {
		t.Helper()
		var page struct {
			Items      []struct{ ID string }
			TotalCount *int
		}
		if code := get(t, srv, path, &page); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		var out []string
		for _, it := range page.Items {
			out = append(out, strings.TrimLeft(strings.TrimPrefix(it.ID, "item_"), "0"))
		}
		return out, page.TotalCount
	}
	// From the index: films 1 to 4, counted by Overfin.
	if got, total := ids("/api/v1/items?kind=movie"); strings.Join(got, ",") != "1,2,3,4" || total == nil || *total != 4 {
		t.Errorf("index: %v %v", got, total)
	}
	// From Jellyfin (any kind): the same, 7 left out by its library; no total.
	if got, total := ids("/api/v1/items"); strings.Join(got, ",") != "1,2,3,4" || total != nil {
		t.Errorf("jellyfin: %v %v", got, total)
	}
	var libs struct{ Items []struct{ ID string } }
	if get(t, srv, "/api/v1/libraries", &libs); len(libs.Items) != 1 || libs.Items[0].ID != "lib_"+films {
		t.Errorf("libraries: %+v", libs)
	}
	if code := get(t, srv, fmt.Sprintf("/api/v1/items/item_%032x", 3), nil); code != 200 {
		t.Errorf("item 3: %d", code)
	}

	// Every library again, and no tag: everything.
	post(t, srv, "/api/v1/profiles/"+ownerID(t, srv)+"/activate", `{}`)
	if code, body := put(t, srv, "PATCH", "/api/v1/profiles/"+partner.ID, `{"libraries":null,"blockedTags":null}`); code != 200 ||
		strings.Contains(body, "libraries") || strings.Contains(body, "blockedTags") {
		t.Errorf("lift: %d %s", code, body)
	}
}

// Items show Overfin's own artwork where it has some, Jellyfin's otherwise.
func TestArtworkOnItems(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	st.PutCatalog(ctx, []store.CatalogItem{
		{ID: fmt.Sprintf("%032x", 3), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetArtwork(ctx, store.ArtworkTarget{Kind: "movie", TMDB: "949"}, map[string]map[string]string{
		"movie/tmdb/949": {"poster": "/heatposter1.jpg", "logo": "/heatlogo123.png"},
	}, time.Now())

	var it struct {
		Images struct {
			Primary   struct{ URL string }
			Logo      struct{ URL string }
			Backdrops []struct{ URL string }
		}
	}
	if code := get(t, srv, fmt.Sprintf("/api/v1/items/item_%032x", 3), &it); code != 200 {
		t.Fatalf("item: %d", code)
	}
	if it.Images.Primary.URL != "/api/v1/images/tmdbart_poster_heatposter1_jpg" || it.Images.Logo.URL != "/api/v1/images/tmdbart_logo_heatlogo123_png" {
		t.Errorf("artwork: %+v", it.Images)
	}
	// A path Overfin doesn't keep is refused before TMDB is asked.
	if code := get(t, srv, "/api/v1/images/tmdbart_poster_inventedpath_jpg", nil); code != 404 {
		t.Errorf("invented path: %d", code)
	}
}
