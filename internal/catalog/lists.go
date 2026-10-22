package catalog

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/overfin/server/internal/seerr"
	"github.com/overfin/server/internal/store"
)

// Lists keeps discovery's lists (trending, popular and upcoming films and
// series) and the titles in them, from Seerr, in Overfin's database, so
// discovery is answered from there, at once, and Seerr is asked in the
// background only.
type Lists struct {
	// Seerr is the current Seerr, nil when it isn't connected.
	Seerr  func() *seerr.Client
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often the lists are read again; Depth, how many of
	// Seerr's pages (20 titles each) each list keeps.
	Every time.Duration
	Depth int

	wake chan struct{}
	now  func() time.Time
}

// Kept is how long a title fetched from TMDB (through Seerr) stays without
// being fetched again: TMDB's terms allow six months at most.
const Kept = 150 * 24 * time.Hour

// NewLists returns the lists' keeper, reading depth pages per list every
// every.
func NewLists(s func() *seerr.Client, st *store.Store, logger *slog.Logger, every time.Duration, depth int) *Lists {
	return &Lists{Seerr: s, Store: st, Logger: logger, Every: every, Depth: depth, wake: make(chan struct{}, 1)}
}

func (l *Lists) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Wake asks for the lists to be read again now, for example right after
// Seerr was connected.
func (l *Lists) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Run reads the lists now, then every Every and whenever woken, until ctx
// ends.
func (l *Lists) Run(ctx context.Context) {
	for {
		if err := l.Refresh(ctx); err != nil && ctx.Err() == nil {
			l.Logger.Error("catalog: reading discovery's lists failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-l.wake:
		case <-time.After(l.Every):
		}
	}
}

// Refresh reads every list from Seerr, keeps their titles, and deletes
// those too old to keep.
func (l *Lists) Refresh(ctx context.Context) error {
	if _, err := l.Store.PurgeTitles(ctx, l.clock().Add(-Kept)); err != nil {
		return err
	}
	s := l.Seerr()
	if s == nil {
		return nil
	}
	for _, list := range seerr.Lists {
		for _, kind := range []string{"movie", "series"} {
			if err := l.read(ctx, s, list, kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Lists) read(ctx context.Context, s *seerr.Client, list, kind string) error {
	at := l.clock()
	var ids []string
	seen := map[string]bool{}
	for page := 1; page <= l.Depth; page++ {
		p, err := s.Discover(ctx, list, kind, page)
		if err != nil {
			return err
		}
		titles := make([]store.Title, 0, len(p.Titles))
		for _, t := range p.Titles {
			st := StoreTitle(t)
			titles = append(titles, st)
			if !seen[st.TMDB] { // a title can move between pages while they're read
				seen[st.TMDB] = true
				ids = append(ids, st.TMDB)
			}
		}
		if err := l.Store.PutTitles(ctx, titles, at); err != nil {
			return err
		}
		if page >= p.TotalPages {
			break
		}
	}
	return l.Store.SetList(ctx, list, kind, ids)
}

// StoreTitle is a title from Seerr as the catalog keeps it.
func StoreTitle(t seerr.Title) store.Title {
	return store.Title{Kind: t.Kind, TMDB: strconv.Itoa(t.TMDBID), Name: t.Name, Year: t.Year, Overview: t.Overview,
		Poster: t.Poster, Backdrop: t.Backdrop, Status: t.Status}
}
