package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/overfin/server/internal/store"
)

func TestListingsFromTheIndex(t *testing.T) {
	var st *store.Store
	srv, _ := newServerWith(t, options{store: &st})
	ctx := context.Background()
	lib := fmt.Sprintf("%032x", 500)
	item := func(n int, name, library string) store.CatalogItem {
		return store.CatalogItem{ID: fmt.Sprintf("%032x", n), Kind: "movie", Name: name, SortName: name, LibraryID: library}
	}
	st.PutCatalog(ctx, []store.CatalogItem{
		item(1, "Charlie", lib), item(2, "alpha", lib), item(3, "Bravo", lib),
		item(4, "Elsewhere", fmt.Sprintf("%032x", 501)),
	}, 1)
	st.RefreshTitleKeys(ctx)
	st.SetCatalogState(ctx, store.CatalogState{Pass: 1})

	type page struct {
		Items      []struct{ ID string }
		NextCursor string
		TotalCount *int
	}
	var out page
	ids := func() []string {
		var s []string
		for _, it := range out.Items {
			s = append(s, it.ID)
		}
		return s
	}
	want := func(ns ...int) string {
		var s []string
		for _, n := range ns {
			s = append(s, fmt.Sprintf("title_item_%032x", n))
		}
		return fmt.Sprint(s)
	}

	// By name, whatever the case, in the library only, counted by Overfin.
	if code := get(t, srv, "/api/v1/titles?library=lib_"+lib+"&kind=movie&limit=2", &out); code != 200 {
		t.Fatalf("listing: %d", code)
	}
	if fmt.Sprint(ids()) != want(2, 3) || out.NextCursor == "" || out.TotalCount == nil || *out.TotalCount != 3 {
		t.Fatalf("first page: %v next %q total %v", ids(), out.NextCursor, out.TotalCount)
	}
	cursor := out.NextCursor
	out = page{}
	get(t, srv, "/api/v1/titles?library=lib_"+lib+"&kind=movie&limit=2&cursor="+cursor, &out)
	if fmt.Sprint(ids()) != want(1) || out.NextCursor != "" || out.TotalCount != nil {
		t.Errorf("second page: %v next %q total %v", ids(), out.NextCursor, out.TotalCount)
	}
	out = page{}
	get(t, srv, "/api/v1/titles?library=lib_"+lib+"&kind=movie&sort=name&order=desc", &out)
	if fmt.Sprint(ids()) != want(1, 3, 2) {
		t.Errorf("descending: %v", ids())
	}

	// What the profile did is Overfin's alone, answered from the index:
	// nothing watched yet, so every film is unwatched.
	out = page{}
	get(t, srv, "/api/v1/titles?library=lib_"+lib+"&kind=movie&played=false", &out)
	if fmt.Sprint(ids()) != want(2, 3, 1) {
		t.Errorf("a personal filter: %v", ids())
	}
}
