package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/C0PT3R/overfin/internal/jellyfin"
	"github.com/C0PT3R/overfin/internal/library"
	"github.com/C0PT3R/overfin/internal/plugin"
	"github.com/C0PT3R/overfin/internal/problem"
	"github.com/C0PT3R/overfin/internal/store"
)

// Watchlist is each profile's list of titles to watch: movies, whole shows,
// seasons or single episodes, as the person chooses. A show on it is the
// show itself, so its new episodes come along. Private to the profile;
// titles are always read with the profile's own token, so one it may not
// see (a kids limit, say) never appears.
type Watchlist struct {
	Library Library
}

func (Watchlist) Name() string { return "watchlist" }

func (wl Watchlist) Register(r *plugin.Registrar) error {
	for pattern, h := range map[string]http.HandlerFunc{
		"GET /api/v1/me/watchlist":         wl.list,
		"GET /api/v1/me/watchlist/{id}":    wl.has,
		"PUT /api/v1/me/watchlist/{id}":    wl.add,
		"DELETE /api/v1/me/watchlist/{id}": wl.remove,
	} {
		if err := r.HandleFunc(pattern, h); err != nil {
			return err
		}
	}
	return nil
}

// watchable are the kinds a watchlist takes.
var watchable = map[string]bool{"movie": true, "series": true, "season": true, "episode": true, "video": true, "musicVideo": true}

func (wl Watchlist) list(w http.ResponseWriter, r *http.Request) {
	if !onlyParams(w, r) {
		return
	}
	l := wl.Library
	p := l.active(w, r)
	if p == nil {
		return
	}
	items, err := watchlistItems(r.Context(), l, p)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	writeOK(w, map[string]any{"items": items})
}

// watchlistItems reads a profile's watchlist from Jellyfin, in its order,
// leaving out what the profile can't see or what no longer exists.
func watchlistItems(ctx context.Context, l Library, p *store.Profile) ([]library.ItemSummary, error) {
	ids, err := l.Sessions.Store.Watchlist(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	found := map[string]library.ItemSummary{}
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		raw, err := l.Jellyfin.ItemsByID(ctx, auth(p), p.JellyfinUserID, batch)
		if err != nil {
			return nil, err
		}
		for i := range raw {
			if s, ok := library.Summary(&raw[i]); ok {
				id, _ := jellyfin.NormalizeID(raw[i].Id)
				found[id] = s
			}
		}
	}
	out := []library.ItemSummary{}
	for _, id := range ids {
		if s, ok := found[id]; ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// itemID reads the {id} of a watchlist route.
func itemID(r *http.Request) (string, bool) {
	return library.Unwrap(library.ItemPrefix, r.PathValue("id"))
}

func (wl Watchlist) has(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	id, ok := itemID(r)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	if has, err := l.Sessions.Store.WatchlistHas(r.Context(), p.ID, id); err != nil {
		problem.Send(w, problem.Internal, "")
	} else if !has {
		problem.Send(w, problem.NotFound, "")
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (wl Watchlist) add(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	id, ok := itemID(r)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	// Only a title this profile can see, and only kinds one watches.
	raw, err := l.Jellyfin.Item(r.Context(), auth(p), p.JellyfinUserID, id)
	if err != nil {
		l.upstreamError(w, err)
		return
	}
	s, ok := library.Summary(raw)
	if !ok || !watchable[s.Kind] {
		problem.Send(w, problem.InvalidRequest, "only movies, shows, seasons, episodes and videos go on a watchlist")
		return
	}
	switch err := l.Sessions.Store.WatchlistAdd(r.Context(), p.ID, id); {
	case errors.Is(err, store.ErrWatchlistFull):
		problem.Send(w, problem.InvalidRequest, "the watchlist is full")
	case err != nil:
		problem.Send(w, problem.Internal, "")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (wl Watchlist) remove(w http.ResponseWriter, r *http.Request) {
	l := wl.Library
	id, ok := itemID(r)
	if !ok {
		problem.Send(w, problem.NotFound, "")
		return
	}
	p := l.active(w, r)
	if p == nil {
		return
	}
	if err := l.Sessions.Store.WatchlistRemove(r.Context(), p.ID, id); err != nil {
		problem.Send(w, problem.Internal, "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
