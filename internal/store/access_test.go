package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A hidden tag hides it whatever the case of any letter, accented ones
// too, as the tags listed and the profile's settings count them.
func TestHiddenTagAnyCase(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Peur", SortName: "a", TMDB: "1", LibraryID: "films", Tags: []string{"épouvante"}},
		{ID: id(1), Kind: "movie", Name: "Effroi", SortName: "b", TMDB: "2", LibraryID: "films", Tags: []string{"Épouvante"}},
		{ID: id(2), Kind: "movie", Name: "Rire", SortName: "c", TMDB: "3", LibraryID: "films"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	a := Access{BlockedTags: []string{"ÉPOUVANTE"}}
	got, err := s.ListCatalog(ctx, ListQuery{Access: &a, Sort: "name", Limit: 50})
	if err != nil || !slices.Equal(got, []string{id(2)}) {
		t.Errorf("listed: %v %v", got, err)
	}
	allowed, _, err := s.AllowedItems(ctx, a, []string{id(0), id(1), id(2)})
	if err != nil || allowed[id(0)] || allowed[id(1)] || !allowed[id(2)] {
		t.Errorf("allowed: %v %v", allowed, err)
	}
	if tags, _ := s.CatalogTags(ctx); len(tags) != 1 {
		t.Errorf("tags: %v", tags)
	}
	if !a.Blocks([]string{"épouvante"}) {
		t.Error("Blocks disagrees")
	}
}

func TestAccessAndListings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id := func(n int) string { return strings.Repeat("0", 31) + string(rune('a'+n)) }
	one, two := 1, 2
	s.PutCatalog(ctx, []CatalogItem{
		{ID: id(0), Kind: "movie", Name: "Heat", SortName: "a", TMDB: "1", LibraryID: "films", People: []string{"p1"}},
		{ID: id(1), Kind: "movie", Name: "Gore", SortName: "b", TMDB: "2", LibraryID: "films", Tags: []string{"Horror"}},
		{ID: id(2), Kind: "series", Name: "Show", SortName: "c", TMDB: "3", LibraryID: "tv", Tags: []string{"horror"}, People: []string{"p1"}},
		{ID: id(3), Kind: "episode", Name: "E1", SortName: "d", SeriesID: id(2), SeasonNumber: &one, EpisodeNumber: &one, LibraryID: "tv"},
		{ID: id(4), Kind: "episode", Name: "E2", SortName: "e", SeriesID: id(2), SeasonNumber: &one, EpisodeNumber: &two, LibraryID: "tv"},
		{ID: id(5), Kind: "series", Name: "Other", SortName: "f", TMDB: "4", LibraryID: "tv"},
	}, 1)
	if err := s.RefreshTitleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	list := func(q ListQuery) []string {
		t.Helper()
		if q.Sort == "" {
			q.Sort = "name"
		}
		q.Limit = 50
		got, err := s.ListCatalog(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Libraries: only those the profile gets; none at all, nothing.
	if got := list(ListQuery{Access: &Access{Libraries: []string{"films"}}}); !slices.Equal(got, []string{id(0), id(1)}) {
		t.Errorf("films only: %v", got)
	}
	if got := list(ListQuery{Access: &Access{Libraries: []string{}}}); len(got) != 0 {
		t.Errorf("no library: %v", got)
	}
	// A tag hides the title, and a series' tag its episodes, whatever the case.
	if got := list(ListQuery{Access: &Access{BlockedTags: []string{"HORROR"}}}); !slices.Equal(got, []string{id(0), id(5)}) {
		t.Errorf("horror hidden: %v", got)
	}
	tags, _ := s.CatalogTags(ctx)
	if len(tags) != 1 {
		t.Errorf("tags: %v", tags)
	}

	acct, prof, _ := s.CreateAccount(ctx, "subject-1", owner("1"))
	_ = acct
	keys, _ := s.TitleKeys(ctx, []string{id(0), id(3), id(4)})
	at := time.Unix(1_000_000, 0)
	// A series is watched once all its episodes are; in progress while one is.
	played := true
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(3)]: id(3)}, true, at)
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &played}); len(got) != 0 {
		t.Errorf("half watched as watched: %v", got)
	}
	s.RecordPlay(ctx, prof.ID, keys[id(4)], id(4), 60_000, false, at.Add(time.Hour))
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, InProgress: true}); !slices.Equal(got, []string{id(2)}) {
		t.Errorf("in progress: %v", got)
	}
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(4)]: id(4)}, true, at.Add(2*time.Hour))
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &played}); !slices.Equal(got, []string{id(2)}) {
		t.Errorf("watched: %v", got)
	}
	unplayed := false
	if got := list(ListQuery{Kinds: []string{"series"}, ProfileID: prof.ID, Played: &unplayed}); !slices.Equal(got, []string{id(5)}) {
		t.Errorf("unwatched: %v", got)
	}
	// Last played first: the series (through its episodes), then the film;
	// what was never played comes last.
	s.SetPlayed(ctx, prof.ID, map[string]string{keys[id(0)]: id(0)}, true, at.Add(-time.Hour))
	got := list(ListQuery{Kinds: []string{"movie", "series"}, ProfileID: prof.ID, Sort: "lastPlayed", Descending: true})
	if len(got) != 4 || got[0] != id(2) || got[1] != id(0) {
		t.Errorf("last played: %v", got)
	}
	// Everything, for shuffling.
	if got := list(ListQuery{Kinds: []string{"movie"}, Limit: -1}); len(got) != 2 {
		t.Errorf("all: %v", got)
	}

	// A profile's access, kept and read back.
	if err := s.SetProfileAccess(ctx, acct.ID, prof.ID, []string{"films"}, []string{"Horror"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Profile(ctx, acct.ID, prof.ID)
	if !slices.Equal(p.Libraries, []string{"films"}) || !slices.Equal(p.BlockedTags, []string{"Horror"}) {
		t.Errorf("access: %+v", p)
	}
	if a := AccessOf(p, "US"); a == nil || !a.GetsLibrary("films") || a.GetsLibrary("tv") || !a.Blocks([]string{"horror"}) {
		t.Errorf("access of: %+v", a)
	}
	s.SetProfileAccess(ctx, acct.ID, prof.ID, nil, nil)
	if p, _ := s.Profile(ctx, acct.ID, prof.ID); p.Libraries != nil || AccessOf(p, "US") != nil {
		t.Errorf("cleared: %+v", p)
	}
}
