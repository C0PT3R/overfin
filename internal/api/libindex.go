package api

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/C0PT3R/overfin-gateway/internal/cursor"
	"github.com/C0PT3R/overfin-gateway/internal/jellyfin"
	"github.com/C0PT3R/overfin-gateway/internal/library"
	"github.com/C0PT3R/overfin-gateway/internal/problem"
	"github.com/C0PT3R/overfin-gateway/internal/store"
)

// Library listings answered from the catalog's index, which is faster than
// asking Jellyfin to list and count each time. Jellyfin still has the last
// word on what the profile sees: each page is read from it, with the
// profile's own token, by the items' IDs, so anything the profile may not
// see is left out (which also brings each item's progress), and the total
// is Jellyfin's own count for the profile (rule 56).
//
// What the index can't answer yet goes to Jellyfin as before: kids
// profiles (their ceiling is Jellyfin's), personal filters (watched,
// favorites, in progress), a person's titles, shuffling, last played, and
// item kinds it doesn't index.

var indexKinds = []string{"movie", "series", "season", "episode"}

// indexQuery is the listing as the index answers it, when it can.
func indexQuery(q url.Values, p *store.Profile) (store.ListQuery, bool) {
	if p.Kind == "kids" {
		return store.ListQuery{}, false
	}
	for _, personal := range []string{"played", "favorite", "inProgress", "person", "seed"} {
		if q.Has(personal) {
			return store.ListQuery{}, false
		}
	}
	lq := store.ListQuery{Sort: q.Get("sort"), Descending: q.Get("order") == "desc", Genres: q["genre"]}
	if lq.Sort == "" {
		lq.Sort = "name"
	}
	if !slices.Contains([]string{"name", "premiereDate", "addedAt", "communityRating"}, lq.Sort) {
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
	if s := q.Get("studio"); s != "" {
		lq.Studios = []string{s}
	}
	lq.YearFrom, _ = strconv.Atoi(q.Get("yearFrom"))
	lq.YearTo, _ = strconv.Atoi(q.Get("yearTo"))
	if s := q.Get("library"); s != "" {
		lq.LibraryID, _ = library.Unwrap(library.LibraryPrefix, s)
	}
	if s := q.Get("parent"); s != "" {
		lq.ParentID, _ = library.Unwrap(library.ItemPrefix, s)
	}
	return lq, true
}

// indexPage serves a listing from the index. jq is the same listing for
// Jellyfin, which counts it for the profile on the first page.
func (l Library) indexPage(w http.ResponseWriter, r *http.Request, p *store.Profile, lq store.ListQuery, jq jellyfin.ItemQuery) {
	q := r.URL.Query()
	limit, ok := parseLimit(q.Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	binding := cursorBinding(r, p.ID)
	offset := 0
	if c := q.Get("cursor"); c != "" {
		payload, err := cursor.Decode(l.CursorKey, binding, c)
		if err != nil {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
		if offset, err = strconv.Atoi(string(payload)); err != nil || offset < 0 {
			problem.Send(w, problem.InvalidCursor, "")
			return
		}
	}
	ctx := r.Context()
	// One more than asked for, to know whether there are more.
	lq.Offset, lq.Limit = offset, limit+1
	ids, err := l.Provisioner.Store.ListCatalog(ctx, lq)
	if err != nil {
		l.Logger.Error("reading the catalog failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	out := itemPage{Items: []library.ItemSummary{}}
	if len(ids) > limit {
		ids = ids[:limit]
		if c, err := cursor.Encode(l.CursorKey, binding, []byte(strconv.Itoa(offset+limit))); err == nil {
			out.NextCursor = c
		}
	}
	if len(ids) > 0 {
		page, err := l.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID, jellyfin.ItemQuery{IDs: ids, Limit: len(ids)})
		if err != nil {
			l.upstreamError(w, err)
			return
		}
		seen := map[string]library.ItemSummary{}
		for i := range page.Items {
			if s, ok := library.Summary(&page.Items[i]); ok {
				seen[s.ID] = s
			}
		}
		for _, id := range ids { // in the index's order
			if s, ok := seen["item_"+id]; ok {
				out.Items = append(out.Items, s)
			}
		}
	}
	if offset == 0 {
		jq.StartIndex, jq.Limit = 0, 0
		count, err := l.Jellyfin.Items(ctx, auth(p), p.JellyfinUserID, jq)
		if err != nil {
			l.upstreamError(w, err)
			return
		}
		total := count.TotalRecordCount
		out.TotalCount = &total
	}
	writeOK(w, out)
}
