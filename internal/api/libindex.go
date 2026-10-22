package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/overfin/server/internal/cursor"
	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
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

var indexKinds = []string{"movie", "series", "season", "episode"}

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
	if s := q.Get("person"); s != "" {
		lq.PersonID, _ = library.Unwrap(library.PersonPrefix, s)
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
	if out.Items, err = l.itemsByID(ctx, p, ids); err != nil {
		l.upstreamError(w, err)
		return
	}
	if offset == 0 && lq.Access != nil {
		total, err := l.Provisioner.Store.CountCatalog(ctx, lq)
		if err != nil {
			l.storeError(w, err)
			return
		}
		out.TotalCount = &total
	} else if offset == 0 {
		jq.StartIndex, jq.Limit = 0, 0
		a, uid, err := l.jf(ctx)
		if err != nil {
			l.upstreamError(w, err)
			return
		}
		count, err := l.Jellyfin.Items(ctx, a, uid, jq)
		if err != nil {
			l.upstreamError(w, err)
			return
		}
		total := count.TotalRecordCount
		out.TotalCount = &total
	}
	writeOK(w, out)
}

// itemsByID reads items from Jellyfin as the profile, by their IDs, in that
// order, with what Overfin keeps about the profile's titles: what the
// profile may not see is left out.
func (l Library) itemsByID(ctx context.Context, p *store.Profile, ids []string) ([]library.ItemSummary, error) {
	out := []library.ItemSummary{}
	if len(ids) == 0 {
		return out, nil
	}
	a, uid, err := l.jf(ctx)
	if err != nil {
		return nil, err
	}
	page, err := l.Jellyfin.Items(ctx, a, uid, jellyfin.ItemQuery{IDs: ids, Limit: len(ids)})
	if err != nil {
		return nil, err
	}
	if page.Items, err = l.keep(ctx, p, page.Items); err != nil {
		return nil, err
	}
	seen := map[string]library.ItemSummary{}
	for i := range page.Items {
		if s, ok := library.Summary(&page.Items[i]); ok {
			seen[s.ID] = s
		}
	}
	for _, id := range ids {
		if s, ok := seen["item_"+id]; ok {
			out = append(out, s)
		}
	}
	return out, l.withStates(ctx, p, out)
}

// idPage serves a page of item IDs from Overfin's own data, with Overfin's
// paging; no total.
func (l Library) idPage(w http.ResponseWriter, r *http.Request, p *store.Profile,
	list func(ctx context.Context, offset, limit int) ([]string, error)) {
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
	ids, err := list(r.Context(), offset, limit+1)
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := itemPage{}
	if len(ids) > limit {
		ids = ids[:limit]
		if c, err := cursor.Encode(l.CursorKey, binding, []byte(strconv.Itoa(offset+limit))); err == nil {
			out.NextCursor = c
		}
	}
	if out.Items, err = l.itemsByID(r.Context(), p, ids); err != nil {
		l.upstreamError(w, err)
		return
	}
	writeOK(w, out)
}
