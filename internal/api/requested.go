package api

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/overfin/server/internal/seerr"
)

// SeerrRequested is what Seerr says is requested and still to come, by
// anyone (in Seerr itself, before Overfin, or by another account), so a
// title says it's requested instead of offering to request it again. Who
// asked is never said (SECURITY.md rule 49). Kept in memory, read again
// in the background when older than requestedFor.
type SeerrRequested struct {
	Logger *slog.Logger

	mu         sync.Mutex
	client     func() *seerr.Client
	byKey      map[string][]seerr.Request // by title key: movie/tmdb/N, series/tmdb/N
	at         time.Time
	refreshing bool
	// gen counts the clients set: a read of an older one isn't kept.
	gen int
}

// requestedFor is how long what Seerr said is kept before it's read again.
const requestedFor = 5 * time.Minute

// SetClient sets where Seerr is (nil when it's disconnected); what was
// known is read again.
func (s *SeerrRequested) SetClient(client func() *seerr.Client) {
	s.mu.Lock()
	s.client, s.byKey, s.at = client, nil, time.Time{}
	s.gen++
	s.refreshing = false
	s.mu.Unlock()
}

// Stale has the next lookup read Seerr again (after a request).
func (s *SeerrRequested) Stale() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

// Lookup returns the open requests for a film or a series, as last read;
// reading Seerr again in the background when that's old.
func (s *SeerrRequested) Lookup(key string) []seerr.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil && !s.refreshing && time.Since(s.at) > requestedFor {
		s.refreshing = true
		go s.refresh(s.gen)
	}
	return s.byKey[key]
}

func (s *SeerrRequested) refresh(gen int) {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	var c *seerr.Client
	if client != nil {
		c = client()
	}
	byKey := map[string][]seerr.Request{}
	var err error
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var open []seerr.Request
		if open, err = c.OpenRequests(ctx); err == nil {
			for _, r := range open {
				key := r.Kind + "/tmdb/" + strconv.Itoa(r.TMDBID)
				byKey[key] = append(byKey[key], r)
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return // Seerr changed meanwhile
	}
	s.refreshing = false
	if err != nil {
		// Tried again at the next lookup after a while; what was known stays.
		s.at = time.Now().Add(-requestedFor + time.Minute)
		if s.Logger != nil {
			s.Logger.Warn("reading Seerr's requests failed", "err", err)
		}
		return
	}
	s.byKey, s.at = byKey, time.Now()
}
