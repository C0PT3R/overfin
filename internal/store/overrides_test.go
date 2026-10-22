package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A film identified by hand: its key and what profiles did with it move to
// the title it is, Jellyfin's next read doesn't undo it, and undoing it
// moves everything back. A copy still the old title keeps what was done.
func TestOverrides(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	_, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	film := CatalogItem{ID: id(0), Kind: "movie", Name: "Wrong", SortName: "w", TMDB: "1"}
	s.PutCatalog(ctx, []CatalogItem{film}, 1)
	s.RefreshTitleKeys(ctx)
	s.RecordPlay(ctx, prof.ID, "movie/tmdb/1", id(0), 0, true, time.Now())
	key := func(itemID string) string {
		var k string
		s.db.QueryRowContext(ctx, `SELECT title_key FROM catalog_items WHERE id = ?`, itemID).Scan(&k)
		return k
	}
	played := func(title string) bool {
		var n int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM profile_titles WHERE title = ? AND played = 1`, title).Scan(&n)
		return n == 1
	}

	if err := s.SetOverride(ctx, id(0), "2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if key(id(0)) != "movie/tmdb/2" || !played("movie/tmdb/2") || played("movie/tmdb/1") {
		t.Errorf("set: %s", key(id(0)))
	}
	s.PutCatalog(ctx, []CatalogItem{film}, 2) // Jellyfin still says 1
	s.RefreshTitleKeys(ctx)
	if o, _ := s.Overrides(ctx); key(id(0)) != "movie/tmdb/2" || o[id(0)] != "2" {
		t.Errorf("after a read: %s %v", key(id(0)), o)
	}
	if err := s.ClearOverride(ctx, id(0)); err != nil {
		t.Fatal(err)
	}
	if key(id(0)) != "movie/tmdb/1" || !played("movie/tmdb/1") {
		t.Errorf("cleared: %s", key(id(0)))
	}

	// Two copies of title 1; one is really 3: the watch stays with 1.
	s.PutCatalog(ctx, []CatalogItem{{ID: id(1), Kind: "movie", Name: "Copy", SortName: "c", TMDB: "1"}}, 2)
	s.RefreshTitleKeys(ctx)
	s.SetOverride(ctx, id(0), "3", time.Now())
	if key(id(0)) != "movie/tmdb/3" || !played("movie/tmdb/1") || played("movie/tmdb/3") {
		t.Errorf("shared: %s", key(id(0)))
	}
	if err := s.SetOverride(ctx, id(5), "3", time.Now()); err != ErrNotFound {
		t.Errorf("unknown item: %v", err)
	}
}
