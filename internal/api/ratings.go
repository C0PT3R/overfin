package api

import (
	"context"

	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/library"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Kids profiles are held to Overfin's own ratings, from TMDB (rule 28):
// each title they see has certifications in the server's ratings country,
// all within the profile's ceiling for its kind. Jellyfin doesn't limit
// them; everything they reach is checked here, and anything Overfin can't
// rate is hidden.

// ceiling is what the profile is held to; nil for profiles other than kids.
func (l Library) ceiling(ctx context.Context, p *store.Profile) (*store.Ceiling, error) {
	if p.Kind != "kids" {
		return nil, nil
	}
	country, err := l.Provisioner.Store.RatingsCountry(ctx)
	if err != nil {
		return nil, err
	}
	return &store.Ceiling{Country: country, Movie: p.MovieCeiling, Series: p.SeriesCeiling}, nil
}

// allowedIDs keeps the library items (by Jellyfin ID) the profile may see,
// in order.
func (l Library) allowedIDs(ctx context.Context, p *store.Profile, ids []string) ([]string, error) {
	c, err := l.ceiling(ctx, p)
	if c == nil || err != nil || len(ids) == 0 {
		return ids, err
	}
	ok, err := l.Provisioner.Store.AllowedItems(ctx, *c, ids)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if ok[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// allowedItems keeps the items the profile may see, in order.
func (l Library) allowedItems(ctx context.Context, p *store.Profile, items []library.ItemSummary) ([]library.ItemSummary, error) {
	if p.Kind != "kids" || len(items) == 0 {
		return items, nil
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if id, ok := library.Unwrap(library.ItemPrefix, it.ID); ok {
			ids = append(ids, id)
		}
	}
	keep, err := l.allowedIDs(ctx, p, ids)
	if err != nil {
		return nil, err
	}
	kept := make(map[string]bool, len(keep))
	for _, id := range keep {
		kept["item_"+id] = true
	}
	out := make([]library.ItemSummary, 0, len(keep))
	for _, it := range items {
		if kept[it.ID] {
			out = append(out, it)
		}
	}
	return out, nil
}

// mayOpen says whether the profile may see an item read from Jellyfin:
// a rated title within its ceiling, or what carries no rating of its own
// (a library, a person). Anything else is hidden from kids.
func (l Library) mayOpen(ctx context.Context, p *store.Profile, it *jellyfin.RawItem) (bool, error) {
	if p.Kind != "kids" {
		return true, nil
	}
	switch it.Type {
	case "CollectionFolder", "UserView", "Person":
		return true, nil
	case "Movie", "Series", "Season", "Episode":
		id, err := jellyfin.NormalizeID(it.Id)
		if err != nil {
			return false, nil
		}
		ids, err := l.allowedIDs(ctx, p, []string{id})
		return len(ids) == 1, err
	}
	return false, nil
}
