package api_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/overfin/server/internal/store"
)

// What a profile does with titles is Overfin's: playbacks record it, marks
// set it, and Continue Watching, Next Up and the filters read it.
func TestPersonalData(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	one, two, three := 1, 2, 3
	st.PutCatalog(ctx, []store.CatalogItem{
		// Item 1: the film the fake Jellyfin plays (two hours long).
		{ID: id(1), Kind: "movie", Name: "Heat", SortName: "Heat", TMDB: "949"},
		{ID: id(10), Kind: "series", Name: "Dark", SortName: "Dark", TMDB: "70523"},
		{ID: id(11), Kind: "episode", Name: "E1", SortName: "E1", SeriesID: id(10), SeasonNumber: &one, EpisodeNumber: &one},
		{ID: id(12), Kind: "episode", Name: "E2", SortName: "E2", SeriesID: id(10), SeasonNumber: &one, EpisodeNumber: &two},
		{ID: id(13), Kind: "episode", Name: "E3", SortName: "E3", SeriesID: id(10), SeasonNumber: &one, EpisodeNumber: &three},
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})
	owner := ownerID(t, srv)
	st.MarkImported(ctx, owner, time.Now())

	type page struct {
		Items []struct {
			ID        string
			UserState *struct {
				PositionMs int64
				Played     bool
				PlayCount  int
				Favorite   bool
			}
		}
	}
	list := func(path string) page {
		t.Helper()
		var out page
		if code := get(t, srv, path, &out); code != 200 {
			t.Fatalf("%s: %d", path, code)
		}
		return out
	}

	// Half-way through the film: in Continue Watching, where it stopped.
	play := startPlayback(t, srv, "Laptop")
	if code, _ := post(t, srv, "/api/v1/playback/"+play+"/progress", `{"positionMs":3600000}`); code != http.StatusNoContent {
		t.Fatalf("progress: %d", code)
	}
	cw := list("/api/v1/items/resume")
	if len(cw.Items) != 1 || cw.Items[0].ID != "item_"+id(1) || cw.Items[0].UserState.PositionMs != 3_600_000 {
		t.Fatalf("Continue Watching: %+v", cw)
	}

	// Played to the end: watched once, out of Continue Watching.
	post(t, srv, "/api/v1/playback/"+play+"/stop", `{"positionMs":7100000,"ended":true}`)
	if !eventually(t, func() bool { return len(list("/api/v1/items/resume").Items) == 0 }) {
		t.Fatal("still in Continue Watching")
	}
	watched := list("/api/v1/items?kind=movie&played=true")
	if len(watched.Items) != 1 || !watched.Items[0].UserState.Played || watched.Items[0].UserState.PlayCount != 1 {
		t.Errorf("watched: %+v", watched)
	}

	// A favorite, marked by the profile.
	if code, body := put(t, srv, "PUT", "/api/v1/items/item_"+id(1)+"/favorite", ""); code != 200 {
		t.Fatalf("favorite: %d %s", code, body)
	}
	if favs := list("/api/v1/items?kind=movie&favorite=true"); len(favs.Items) != 1 || !favs.Items[0].UserState.Favorite {
		t.Errorf("favorites: %+v", favs)
	}
	if none := list("/api/v1/items?kind=movie&favorite=false"); len(none.Items) != 0 {
		t.Errorf("not favorites: %+v", none)
	}

	// Out of Continue Watching, by hand: watched or not stays as it was.
	play = startPlayback(t, srv, "Laptop")
	post(t, srv, "/api/v1/playback/"+play+"/stop", `{"positionMs":1800000}`)
	if !eventually(t, func() bool { return len(list("/api/v1/items/resume").Items) == 1 }) {
		t.Fatal("not back in Continue Watching")
	}
	if code, _ := put(t, srv, "DELETE", "/api/v1/items/item_"+id(1)+"/progress", ""); code != 200 {
		t.Fatalf("forget progress: %d", code)
	}
	if cw := list("/api/v1/items/resume"); len(cw.Items) != 0 {
		t.Errorf("still in Continue Watching: %+v", cw)
	}
	if w := list("/api/v1/items?kind=movie&played=true"); len(w.Items) != 1 {
		t.Errorf("no longer watched: %+v", w)
	}

	// Next Up: after episode 1, episode 2; once 2 is marked watched, 3.
	st.SetPlayed(ctx, owner, map[string]string{"series/tmdb/70523/s1/e1": id(11)}, true, time.Now())
	if next := list("/api/v1/items/next-up"); len(next.Items) != 1 || next.Items[0].ID != "item_"+id(12) {
		t.Fatalf("next up: %+v", next)
	}
	st.SetPlayed(ctx, owner, map[string]string{"series/tmdb/70523/s1/e2": id(12)}, true, time.Now().Add(time.Second))
	if next := list("/api/v1/items/next-up"); len(next.Items) != 1 || next.Items[0].ID != "item_"+id(13) {
		t.Errorf("next up after 2: %+v", next)
	}

	// The profile forgets it all.
	if code, _ := put(t, srv, "DELETE", "/api/v1/me/history", ""); code != http.StatusNoContent {
		t.Fatalf("clear: %d", code)
	}
	if all, _ := st.AllPersonal(ctx, owner); len(all) != 0 {
		t.Errorf("kept after clearing: %+v", all)
	}
}
