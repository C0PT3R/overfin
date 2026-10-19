package images

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

// ---- Encoded image cache ------------------------------------------------------

type entry struct {
	data        []byte
	contentType string
}

// lru keeps the most recently used images, up to a total size in bytes.
type lru struct {
	mu       sync.Mutex
	max, cur int
	order    *list.List // front = most recent
	items    map[string]*list.Element
}

type lruItem struct {
	key string
	val entry
}

func newLRU(maxBytes int) *lru {
	return &lru{max: maxBytes, order: list.New(), items: make(map[string]*list.Element)}
}

func (c *lru) get(key string) (entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return entry{}, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*lruItem).val, true
}

func (c *lru) put(key string, val entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(val.data) > c.max {
		return // larger than the whole cache: don't keep it
	}
	if el, ok := c.items[key]; ok {
		c.cur -= len(el.Value.(*lruItem).val.data)
		c.order.Remove(el)
		delete(c.items, key)
	}
	c.items[key] = c.order.PushFront(&lruItem{key: key, val: val})
	c.cur += len(val.data)
	for c.cur > c.max {
		oldest := c.order.Back()
		it := oldest.Value.(*lruItem)
		c.order.Remove(oldest)
		delete(c.items, it.key)
		c.cur -= len(it.val.data)
	}
}

// ---- Access decisions -----------------------------------------------------------

// accessCache remembers, for a short time, that a profile may see an item,
// so a page of posters doesn't cost a Jellyfin call per poster. Refusals are
// never cached: a new permission takes effect at once, and a revoked one
// within ttl.
type accessCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	max     int
	entries map[string]access
}

// access is one remembered permission, with the item's image tags.
type access struct {
	expires time.Time
	tags    map[string]string
}

func newAccessCache(ttl time.Duration, max int) *accessCache {
	return &accessCache{ttl: ttl, max: max, entries: make(map[string]access)}
}

func (a *accessCache) allowed(key string, now time.Time) (map[string]string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.entries[key]
	if ok && now.Before(e.expires) {
		return e.tags, true
	}
	delete(a.entries, key)
	return nil, false
}

func (a *accessCache) forget(prefix string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.entries {
		if strings.HasPrefix(k, prefix) {
			delete(a.entries, k)
		}
	}
}

func (a *accessCache) allow(key string, tags map[string]string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.entries) >= a.max {
		// Full: drop expired entries, and if that isn't enough, start over.
		// Losing entries only costs extra Jellyfin calls, never safety.
		for k, e := range a.entries {
			if !now.Before(e.expires) {
				delete(a.entries, k)
			}
		}
		if len(a.entries) >= a.max {
			a.entries = make(map[string]access)
		}
	}
	a.entries[key] = access{expires: now.Add(a.ttl), tags: tags}
}
