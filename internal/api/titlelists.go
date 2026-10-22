package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/cursor"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Lists of titles (milestone 5): a library's, what's new, Continue
// Watching. Every one is answered from Overfin's own data, and gives
// titles, each once.

type titlePage struct {
	Items      []library.TitleSummary `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	TotalCount *int                   `json:"totalCount,omitempty"`
	Seed       string                 `json:"seed,omitempty"`
}

// titlesOfItems is library items' titles, in the items' order, each once.
func (l Library) titlesOfItems(ctx context.Context, p *store.Profile, ids []string) ([]library.TitleSummary, error) {
	keys, err := l.Provisioner.Store.TitleKeys(ctx, ids)
	if err != nil {
		return nil, err
	}
	ordered := make([]string, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, keys[id])
	}
	return l.titleSummaries(ctx, p, ordered)
}

// pageStart reads where a page starts from its cursor ("" for the first),
// or writes the problem and returns false. With seeded, the cursor also
// carries the seed of a shuffle: "start:seed".
func (l Library) pageStart(w http.ResponseWriter, r *http.Request, p *store.Profile, seeded bool) (start int, seed string, ok bool) {
	c := r.URL.Query().Get("cursor")
	if c == "" {
		return 0, "", true
	}
	payload, err := cursor.Decode(l.CursorKey, cursorBinding(r, p.ID), c)
	if err != nil {
		problem.Send(w, problem.InvalidCursor, "")
		return 0, "", false
	}
	s := string(payload)
	if seeded {
		var found bool
		if s, seed, found = strings.Cut(s, ":"); !found || !seedValue.MatchString(seed) {
			problem.Send(w, problem.InvalidCursor, "")
			return 0, "", false
		}
	}
	if start, err = strconv.Atoi(s); err != nil || start < 0 {
		problem.Send(w, problem.InvalidCursor, "")
		return 0, "", false
	}
	return start, seed, true
}

// nextCursor is the cursor of the page starting at start.
func (l Library) nextCursor(r *http.Request, p *store.Profile, start int, seed string) string {
	payload := strconv.Itoa(start)
	if seed != "" {
		payload += ":" + seed
	}
	c, _ := cursor.Encode(l.CursorKey, cursorBinding(r, p.ID), []byte(payload))
	return c
}

// titles serves a library's titles, filtered and sorted as asked, from the
// index: films and series (or seasons and episodes, under a parent).
func (l Library) titles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !onlyParams(w, r, "library", "kind", "parent", "genre", "person", "studio", "yearFrom", "yearTo",
		"played", "favorite", "inProgress", "sort", "order", "seed", "limit", "cursor") {
		return
	}
	jq, ok := parseItemQuery(q)
	limit, limitOK := parseLimit(q.Get("limit"))
	lq, indexed := indexQuery(q, true)
	if !ok || !limitOK || !indexed {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	out := titlePage{Items: []library.TitleSummary{}}
	if !l.indexReady(r) {
		writeOK(w, out)
		return
	}
	a, err := l.access(ctx, p)
	if err != nil {
		l.storeError(w, err)
		return
	}
	lq.ProfileID, lq.Access = p.ID, a
	st := l.Provisioner.Store

	if jq.SortBy == "random" {
		// The same seed always gives the same order: a reload, or the next
		// page, shows the same shuffle.
		start, seed, ok := l.pageStart(w, r, p, true)
		if !ok {
			return
		}
		if s := q.Get("seed"); s != "" {
			if seed != "" && seed != s {
				problem.Send(w, problem.InvalidCursor, "")
				return
			}
			seed = s
		}
		if seed == "" {
			seed = newSeed()
		}
		lq.Sort, lq.Offset, lq.Limit = "name", 0, -1
		ids, err := st.ListCatalog(ctx, lq)
		if err != nil {
			l.storeError(w, err)
			return
		}
		shuffle(ids, seed)
		total := len(ids)
		out.Seed, out.TotalCount = seed, &total
		if start < total {
			end := min(start+limit, total)
			if out.Items, err = l.titlesOfItems(ctx, p, ids[start:end]); err != nil {
				l.storeError(w, err)
				return
			}
			if end < total {
				out.NextCursor = l.nextCursor(r, p, end, seed)
			}
		}
		writeOK(w, out)
		return
	}

	start, _, ok := l.pageStart(w, r, p, false)
	if !ok {
		return
	}
	// One more than asked for, to know whether there are more.
	lq.Offset, lq.Limit = start, limit+1
	ids, err := st.ListCatalog(ctx, lq)
	if err != nil {
		l.storeError(w, err)
		return
	}
	if len(ids) > limit {
		ids = ids[:limit]
		out.NextCursor = l.nextCursor(r, p, start+limit, "")
	}
	if out.Items, err = l.titlesOfItems(ctx, p, ids); err != nil {
		l.storeError(w, err)
		return
	}
	if start == 0 {
		total, err := st.CountCatalog(ctx, lq)
		if err != nil {
			l.storeError(w, err)
			return
		}
		out.TotalCount = &total
	}
	writeOK(w, out)
}

// latestTitles is what's new in a library (or in all): films and series,
// a series by its newest episode.
func (l Library) latestTitles(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "library", "limit") {
		return
	}
	q := r.URL.Query()
	limit, ok := parseLimit(q.Get("limit"))
	var lib string
	if s := q.Get("library"); s != "" && ok {
		lib, ok = library.Unwrap(library.LibraryPrefix, s)
	}
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	ctx := r.Context()
	out := titlePage{Items: []library.TitleSummary{}}
	if !l.indexReady(r) {
		writeOK(w, out)
		return
	}
	a, err := l.access(ctx, p)
	if err != nil {
		l.storeError(w, err)
		return
	}
	ids, err := l.Provisioner.Store.ListCatalog(ctx, store.ListQuery{LibraryID: lib,
		Kinds: []string{"movie", "series"}, Sort: "latest", Descending: true, Access: a, Limit: limit})
	if err != nil {
		l.storeError(w, err)
		return
	}
	if out.Items, err = l.titlesOfItems(ctx, p, ids); err != nil {
		l.storeError(w, err)
		return
	}
	writeOK(w, out)
}

// continueTitles is Continue Watching: what the profile is in the middle
// of, and each followed series' next episode, a library's or not (to
// request then), the last watched first.
func (l Library) continueTitles(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r, "limit", "cursor") {
		return
	}
	limit, ok := parseLimit(r.URL.Query().Get("limit"))
	if !ok {
		problem.Send(w, problem.InvalidRequest, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	start, _, ok := l.pageStart(w, r, p, false)
	if !ok {
		return
	}
	ctx := r.Context()
	keys, err := l.Provisioner.Store.ContinueTitles(ctx, p.ID, today(), start+limit+1)
	if err != nil {
		l.storeError(w, err)
		return
	}
	out := titlePage{Items: []library.TitleSummary{}}
	if start < len(keys) {
		keys = keys[start:]
		if len(keys) > limit {
			keys = keys[:limit]
			out.NextCursor = l.nextCursor(r, p, start+limit, "")
		}
		if out.Items, err = l.titleSummaries(ctx, p, keys); err != nil {
			l.storeError(w, err)
			return
		}
	}
	writeOK(w, out)
}
