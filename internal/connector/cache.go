package connector

import (
	"context"
	"sync"
	"time"
)

// Directory is a connector's view of people, by the user ID logins carry.
type Directory interface {
	People(ctx context.Context) (map[string]Person, error)
}

// keepFor is how long a Cache keeps what its directory said.
const keepFor = 2 * time.Minute

// Cache keeps what a directory said for two minutes, in memory only (rule
// 40), so the control panel and requests don't ask the identity provider
// at every turn. A failure isn't kept: the next call asks again.
type Cache struct {
	Directory Directory
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	mu     sync.Mutex
	people map[string]Person
	at     time.Time
}

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) People(ctx context.Context) (map[string]Person, error) {
	c.mu.Lock()
	if c.people != nil && c.now().Sub(c.at) < keepFor {
		people := c.people
		c.mu.Unlock()
		return people, nil
	}
	c.mu.Unlock()
	people, err := c.Directory.People(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.people, c.at = people, c.now()
	c.mu.Unlock()
	return people, nil
}
