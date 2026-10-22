package api

import (
	"net/url"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/store"
)

// Library listings answered from the catalog's index, which is faster than
// asking Jellyfin to list and count each time. Jellyfin still has the last
// word on what the profile sees: each page is read from it, with the
// profile's own token, by the items' IDs, so anything the profile may not
// see is left out (which also brings each item's progress), and the total
// is Jellyfin's own count for the profile (rule 56).
//
// What the index can't answer goes to Jellyfin as before: personal
// filters and last played before the profile's data is Overfin's, and item
// kinds it doesn't index (music, books…). A limited profile's listings
// keep only what it may see, and their totals are Overfin's own count.

var indexKinds = []string{"movie", "series"}

// indexQuery is the listing as the index answers it, when it can.
// imported says whether the profile's own data is Overfin's yet (else
// personal filters go to Jellyfin).
func indexQuery(q url.Values, imported bool) (store.ListQuery, bool) {
	// What the profile did is Overfin's once its data is imported.
	personal := q.Has("played") || q.Has("favorite") || q.Has("inProgress") || q.Get("sort") == "lastPlayed"
	if personal && !imported {
		return store.ListQuery{}, false
	}
	lq := store.ListQuery{Sort: q.Get("sort"), Descending: q.Get("order") == "desc", Genres: q["genre"]}
	if lq.Sort == "" {
		lq.Sort = "name"
	}
	if !slices.Contains([]string{"name", "premiereDate", "addedAt", "communityRating", "lastPlayed", "random"}, lq.Sort) {
		return store.ListQuery{}, false
	}
	if len(q["kind"]) == 0 {
		return store.ListQuery{}, false // any kind: some the index doesn't have
	}
	for _, k := range q["kind"] {
		if !slices.Contains(indexKinds, k) {
			return store.ListQuery{}, false
		}
		lq.Kinds = append(lq.Kinds, k)
	}
	lq.YearFrom, _ = strconv.Atoi(q.Get("yearFrom"))
	lq.YearTo, _ = strconv.Atoi(q.Get("yearTo"))
	if s := q.Get("library"); s != "" {
		lq.LibraryID, _ = library.Unwrap(library.LibraryPrefix, s)
	}
	if personal {
		for param, dst := range map[string]**bool{"played": &lq.Played, "favorite": &lq.Favorite} {
			if s := q.Get(param); s != "" {
				b, _ := strconv.ParseBool(s) // checked by parseItemQuery
				*dst = &b
			}
		}
		lq.InProgress, _ = strconv.ParseBool(q.Get("inProgress"))
	}
	return lq, true
}
