// Package ratings keeps Overfin's own content ratings, from TMDB: each
// country's scales of certifications, and the certifications of every
// title in the library or in discovery, in Overfin's database, refreshed
// in the background. Kids profiles are held to them (SECURITY.md rule 28).
//
// TMDB is reached through Seerr, which needs no TMDB key: Overfin never
// asks operators for one. Without Seerr, no title is rated, and kids
// profiles see nothing.
package ratings

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/C0PT3R/overfin-server/internal/seerr"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Source is where TMDB's certifications come from.
type Source interface {
	// Scales returns the scales for films ("movie") or series ("series"),
	// by country.
	Scales(ctx context.Context, kind string) (map[string][]store.RatingLevel, error)
	// Ratings returns a title's certifications, by country; ErrUnknown
	// when TMDB doesn't know the title.
	Ratings(ctx context.Context, kind string, tmdbID int) (map[string][]string, error)
}

// ErrUnknown: TMDB doesn't know the title.
var ErrUnknown = errors.New("ratings: unknown title")

// Seerr is Seerr as a Source.
type Seerr struct{ Client *seerr.Client }

func (s Seerr) Scales(ctx context.Context, kind string) (map[string][]store.RatingLevel, error) {
	raw, err := s.Client.Certifications(ctx, kind)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]store.RatingLevel, len(raw))
	for country, levels := range raw {
		for _, l := range levels {
			out[country] = append(out[country], store.RatingLevel{Certification: l.Certification, Meaning: l.Meaning, Position: l.Order})
		}
	}
	return out, nil
}

func (s Seerr) Ratings(ctx context.Context, kind string, tmdbID int) (map[string][]string, error) {
	r, err := s.Client.Ratings(ctx, kind, tmdbID)
	if errors.Is(err, seerr.ErrNotFound) {
		return nil, ErrUnknown
	}
	return r, err
}

// Keeper fetches scales and titles' certifications in the background.
type Keeper struct {
	// Source is the current source, nil when there's none.
	Source func() Source
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often it looks for titles to fetch; Pause, how long it
	// waits between two titles, to go easy on TMDB.
	Every time.Duration
	Pause time.Duration

	wake chan struct{}
	now  func() time.Time
}

// Kept is how long certifications stay without being fetched again: TMDB's
// terms allow six months at most. They're fetched again after Fresh.
const (
	Kept  = 150 * 24 * time.Hour
	Fresh = 120 * 24 * time.Hour
)

// NewKeeper returns the keeper.
func NewKeeper(source func() Source, st *store.Store, logger *slog.Logger, every, pause time.Duration) *Keeper {
	return &Keeper{Source: source, Store: st, Logger: logger, Every: every, Pause: pause, wake: make(chan struct{}, 1)}
}

func (k *Keeper) clock() time.Time {
	if k.now != nil {
		return k.now()
	}
	return time.Now()
}

// Wake asks for a pass now, for example right after Seerr was connected.
func (k *Keeper) Wake() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// Run makes a pass now, then every Every and whenever woken, until ctx
// ends.
func (k *Keeper) Run(ctx context.Context) {
	for {
		if err := k.Once(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Error("ratings: fetching certifications failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-k.wake:
		case <-time.After(k.Every):
		}
	}
}

// Once deletes what's too old to keep, fetches the scales, then every
// title not fetched for a while.
func (k *Keeper) Once(ctx context.Context) error {
	if err := k.Store.PurgeRatings(ctx, k.clock().Add(-Kept)); err != nil {
		return err
	}
	src := k.Source()
	if src == nil {
		return nil
	}
	for _, kind := range []string{"movie", "series"} {
		scales, err := src.Scales(ctx, kind)
		if err != nil {
			return err
		}
		if err := k.Store.SetRatingScales(ctx, kind, scales); err != nil {
			return err
		}
	}
	fetched := 0
	defer func() {
		if fetched > 0 {
			k.Logger.Info("ratings: titles' certifications fetched", "titles", fetched)
		}
	}()
	// A title Seerr fails on is left for the next pass, and the others go
	// on; ten failures in a row mean Seerr itself is unwell, and the pass
	// stops there.
	failed := map[store.RatedTitle]bool{}
	var lastErr error
	for streak := 0; ; {
		todo, err := k.Store.TitlesToRate(ctx, k.clock().Add(-Fresh), 100+len(failed))
		if err != nil {
			return err
		}
		progress := false
		for _, t := range todo {
			if failed[t] {
				continue
			}
			progress = true
			// A title TMDB doesn't know has no certification: it's kept as
			// such (hidden from kids), and not asked for again until later.
			var r map[string][]string
			if id, err := strconv.Atoi(t.TMDB); err == nil {
				r, err = src.Ratings(ctx, t.Kind, id)
				switch {
				case errors.Is(err, seerr.ErrUnauthorized), ctx.Err() != nil:
					return err
				case err != nil && !errors.Is(err, ErrUnknown):
					failed[t], lastErr = true, err
					if streak++; streak == 10 {
						return err
					}
					continue
				}
			}
			streak = 0
			if err := k.Store.SetTitleRatings(ctx, t, r, k.clock()); err != nil {
				return err
			}
			fetched++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(k.Pause):
			}
		}
		if !progress {
			if len(failed) > 0 {
				k.Logger.Warn("ratings: some titles couldn't be fetched; tried again next time", "titles", len(failed), "err", lastErr)
			}
			return nil
		}
	}
}
