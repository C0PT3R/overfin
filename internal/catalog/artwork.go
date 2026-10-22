package catalog

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/overfin/server/internal/store"
	"github.com/overfin/server/internal/tmdb"
)

// ArtworkSource is where the library's artwork comes from: TMDB itself.
type ArtworkSource interface {
	Artwork(ctx context.Context, kind string, tmdbID int) (*tmdb.Artwork, error)
	Season(ctx context.Context, seriesID, season int) (*tmdb.SeasonArtwork, error)
}

// Artwork keeps the library's artwork from TMDB in Overfin's database: each
// film's and series' poster, backdrop and logo, its seasons' posters and its
// episodes' stills, by title key, refreshed in the background within TMDB's
// six months.
type Artwork struct {
	// Source is the current source, nil when there's none.
	Source func() ArtworkSource
	Store  *store.Store
	Logger *slog.Logger
	// Every is how often it looks for titles to fetch; Pause, how long it
	// waits between two requests, to go easy on TMDB.
	Every time.Duration
	Pause time.Duration

	wake chan struct{}
	now  func() time.Time
}

// ArtworkFresh is how long artwork stays before it's fetched again.
const ArtworkFresh = 120 * 24 * time.Hour

// NewArtwork returns the artwork's keeper.
func NewArtwork(source func() ArtworkSource, st *store.Store, logger *slog.Logger, every, pause time.Duration) *Artwork {
	return &Artwork{Source: source, Store: st, Logger: logger, Every: every, Pause: pause, wake: make(chan struct{}, 1)}
}

func (a *Artwork) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

// Wake asks for a pass now, for example after TMDB's language changed.
func (a *Artwork) Wake() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Run makes a pass now, then every Every and whenever woken, until ctx
// ends.
func (a *Artwork) Run(ctx context.Context) {
	for {
		if err := a.Once(ctx); err != nil && ctx.Err() == nil {
			a.Logger.Error("catalog: fetching artwork failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-time.After(a.Every):
		}
	}
}

// Once deletes what's too old to keep, then fetches every title's artwork
// not fetched for a while. A title TMDB fails on is left for the next pass;
// ten failures in a row stop this one.
func (a *Artwork) Once(ctx context.Context) error {
	if err := a.Store.PurgeArtwork(ctx, a.clock().Add(-Kept)); err != nil {
		return err
	}
	src := a.Source()
	if src == nil {
		return nil
	}
	fetched, streak := 0, 0
	failed := map[string]bool{}
	defer func() {
		if fetched > 0 {
			a.Logger.Info("catalog: artwork fetched", "titles", fetched)
		}
		if len(failed) > 0 {
			a.Logger.Warn("catalog: some titles' artwork couldn't be fetched; tried again next time", "titles", len(failed))
		}
	}()
	for {
		todo, err := a.Store.ArtworkToFetch(ctx, a.clock().Add(-ArtworkFresh), 100+len(failed))
		if err != nil {
			return err
		}
		progress := false
		for _, t := range todo {
			if failed[t.Key()] {
				continue
			}
			progress = true
			images, err := a.fetch(ctx, src, t)
			switch {
			case errors.Is(err, tmdb.ErrUnauthorized), ctx.Err() != nil:
				return err
			case err != nil:
				failed[t.Key()] = true
				if streak++; streak == 10 {
					return err
				}
				continue
			}
			streak = 0
			if err := a.Store.SetArtwork(ctx, t, images, a.clock()); err != nil {
				return err
			}
			fetched++
		}
		if !progress {
			return nil
		}
	}
}

// fetch reads a title's artwork, its seasons' included, by title key.
func (a *Artwork) fetch(ctx context.Context, src ArtworkSource, t store.ArtworkTarget) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	id, err := strconv.Atoi(t.TMDB)
	if err != nil {
		return out, nil
	}
	art, err := src.Artwork(ctx, t.Kind, id)
	if errors.Is(err, tmdb.ErrNotFound) {
		return out, nil // no artwork: Jellyfin's, if any, is shown
	}
	if err != nil {
		return nil, err
	}
	out[t.Key()] = map[string]string{"poster": art.Poster, "backdrop": art.Backdrop, "logo": art.Logo}
	if err := a.pause(ctx); err != nil {
		return nil, err
	}
	for _, n := range t.Seasons {
		season, err := src.Season(ctx, id, n)
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		sk := t.Key() + "/s" + strconv.Itoa(n)
		out[sk] = map[string]string{"poster": season.Poster}
		for e, still := range season.Stills {
			out[sk+"/e"+strconv.Itoa(e)] = map[string]string{"still": still}
		}
		if err := a.pause(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *Artwork) pause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(a.Pause):
		return nil
	}
}
