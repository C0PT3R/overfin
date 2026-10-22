package api

import (
	"context"
	"sync"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// What each profile sees is Overfin's to decide (rule 28): kids profiles
// are held to Overfin's own ratings, from TMDB (each title they see has
// certifications in the server's ratings country, all within the
// profile's ceiling for its kind), and any profile may be limited to some
// libraries and have tags hidden from it. Jellyfin limits none of it:
// everything a profile reaches is checked here, and what Overfin can't
// check is hidden from a limited profile.

// access is what the profile may see; nil when it may see everything.
func (l Library) access(ctx context.Context, p *store.Profile) (*store.Access, error) {
	country := ""
	if p.Kind == "kids" {
		var err error
		if country, err = l.Provisioner.Store.RatingsCountry(ctx); err != nil {
			return nil, err
		}
	}
	return store.AccessOf(p, country), nil
}

// ceiling is the ratings a kids profile is held to; nil for others.
func (l Library) ceiling(ctx context.Context, p *store.Profile) (*store.Ceiling, error) {
	a, err := l.access(ctx, p)
	if a == nil || err != nil {
		return nil, err
	}
	return a.Ceiling, nil
}

// prefilter drops, among item IDs, those the index says the profile may
// not see, before anything is asked of Jellyfin; keep still checks what's
// left.
func (l Library) prefilter(ctx context.Context, a *store.Access, ids []string) ([]string, error) {
	if a == nil || len(ids) == 0 {
		return ids, nil
	}
	allowed, indexed, err := l.Provisioner.Store.AllowedItems(ctx, *a, ids)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		// What the index doesn't have can't be rated: never for kids.
		if allowed[id] || (!indexed[id] && a.Ceiling == nil) {
			out = append(out, id)
		}
	}
	return out, nil
}

// keep returns, of items read from Jellyfin, those the profile may see,
// in order.
func (l Library) keep(ctx context.Context, p *store.Profile, items []jellyfin.RawItem) ([]jellyfin.RawItem, error) {
	a, err := l.access(ctx, p)
	if a == nil || err != nil || len(items) == 0 {
		return items, err
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if id, err := jellyfin.NormalizeID(it.Id); err == nil {
			ids = append(ids, id)
		}
	}
	allowed, indexed, err := l.Provisioner.Store.AllowedItems(ctx, *a, ids)
	if err != nil {
		return nil, err
	}
	out := make([]jellyfin.RawItem, 0, len(items))
	for _, it := range items {
		id, err := jellyfin.NormalizeID(it.Id)
		if err != nil || a.Blocks(it.Tags) {
			continue
		}
		ok := false
		switch {
		case it.Type == "CollectionFolder" || it.Type == "UserView":
			ok = a.GetsLibrary(id)
		case it.Type == "Person":
			ok = true // people carry no rating, library or tag of their own
		case indexed[id]:
			ok = allowed[id]
		case a.Ceiling != nil:
			// Not in the index, so not rated: hidden from kids.
		case a.Libraries == nil:
			ok = true
		default:
			lib, err := l.libraryOf(ctx, p, id)
			if err != nil {
				return nil, err
			}
			ok = lib != "" && a.GetsLibrary(lib)
		}
		if ok {
			out = append(out, it)
		}
	}
	return out, nil
}

// mayOpen says whether the profile may see an item read from Jellyfin.
func (l Library) mayOpen(ctx context.Context, p *store.Profile, it *jellyfin.RawItem) (bool, error) {
	kept, err := l.keep(ctx, p, []jellyfin.RawItem{*it})
	return len(kept) == 1, err
}

// libraryOf is the library an item the index doesn't have is in, asked of
// Jellyfin once and remembered a while.
func (l Library) libraryOf(ctx context.Context, p *store.Profile, id string) (string, error) {
	if lib, ok := l.Folders.get(id); ok {
		return lib, nil
	}
	lib, err := l.Jellyfin.LibraryOf(ctx, auth(p), p.JellyfinUserID, id)
	if err != nil {
		if jellyfin.IsStatus(err, 404) {
			return "", nil
		}
		return "", err
	}
	l.Folders.put(id, lib)
	return lib, nil
}

// FolderCache remembers which library items the index doesn't have (music,
// books…) are in. A nil FolderCache remembers nothing.
type FolderCache struct {
	mu sync.Mutex
	m  map[string]folderEntry
}

type folderEntry struct {
	library string
	at      time.Time
}

// folderFor is how long an item's library is remembered; folderMax, how
// many items at most.
const (
	folderFor = 6 * time.Hour
	folderMax = 100_000
)

func NewFolderCache() *FolderCache { return &FolderCache{m: map[string]folderEntry{}} }

func (c *FolderCache) get(id string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[id]
	if !ok || time.Since(e.at) > folderFor {
		return "", false
	}
	return e.library, true
}

func (c *FolderCache) put(id, library string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= folderMax {
		c.m = map[string]folderEntry{}
	}
	c.m[id] = folderEntry{library: library, at: time.Now()}
}
