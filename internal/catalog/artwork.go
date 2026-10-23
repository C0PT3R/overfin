package catalog

import (
	"context"
	"log/slog"
	"time"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/store"
)

// LibraryArtwork keeps the library titles' artwork on disk, ahead of time
// (images.Pipeline.Keep), in the background: the pictures their pages and
// cards show, at once, without waiting for TMDB. Titles no library has
// get theirs when first shown.
type LibraryArtwork struct {
	Store  *store.Store
	Images *images.Pipeline
	Logger *slog.Logger
	// Every is how often it looks again: new titles get their artwork
	// then.
	Every time.Duration
}

// Run makes a pass now, then every Every, until ctx ends.
func (k *LibraryArtwork) Run(ctx context.Context) {
	for {
		if err := k.Once(ctx); err != nil && ctx.Err() == nil {
			k.Logger.Error("catalog: keeping the library's artwork failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(k.Every):
		}
	}
}

// Once keeps every library title's artwork, fetched again after
// TitlesFresh, within what TMDB lets Overfin keep (Kept).
func (k *LibraryArtwork) Once(ctx context.Context) error {
	keys, err := k.Store.LibraryTitleKeys(ctx)
	if err != nil {
		return err
	}
	var wanted []images.TMDBRef
	for start := 0; start < len(keys); start += 500 {
		infos, err := k.Store.TitleInfos(ctx, keys[start:min(start+500, len(keys))], time.Now().Format(time.DateOnly), "")
		if err != nil {
			return err
		}
		for _, t := range infos {
			if t.Ref.Kind == "episode" {
				// Its series' pictures come with the series.
				wanted = append(wanted, images.TMDBRef{Type: "still", Path: t.Still})
				continue
			}
			wanted = append(wanted, images.TMDBRef{Type: "poster", Path: t.Poster})
			if t.Ref.Kind != "season" {
				wanted = append(wanted, images.TMDBRef{Type: "backdrop", Path: t.Backdrop},
					images.TMDBRef{Type: "logo", Path: t.Logo})
			}
		}
	}
	fetched, deleted, err := k.Images.Keep(ctx, wanted, time.Now().Add(-TitlesFresh))
	if fetched > 0 || deleted > 0 {
		k.Logger.Info("catalog: the library's artwork kept", "fetched", fetched, "deleted", deleted)
	}
	return err
}
