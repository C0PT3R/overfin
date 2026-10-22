package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/library"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Library items' artwork is Overfin's own, from TMDB (catalog.Artwork),
// wherever it has some: a title looks the same wherever it's stored.
// Jellyfin's own images stay for what TMDB has none of (titles it doesn't
// know, images placed next to the files).

var (
	posterAspect = 2.0 / 3
	wideAspect   = 16.0 / 9
)

// withArtwork puts Overfin's artwork on items, slot by slot.
func (l Library) withArtwork(ctx context.Context, items []library.ItemSummary) error {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		if id, ok := library.Unwrap(library.ItemPrefix, it.ID); ok {
			ids = append(ids, id)
		}
	}
	st := l.Provisioner.Store
	keys, err := st.TitleKeys(ctx, ids)
	if err != nil {
		return err
	}
	// Each item's own key, and its series' for seasons and episodes.
	var want []string
	for _, k := range keys {
		if strings.HasPrefix(k, "item/") {
			continue
		}
		want = append(want, k)
		if sk := seriesKey(k); sk != "" {
			want = append(want, sk)
		}
	}
	if len(want) == 0 {
		return nil
	}
	art, err := st.Artwork(ctx, want)
	if err != nil {
		return err
	}
	ref := func(typ, path string, aspect *float64) *library.ImageRef {
		if id := images.ArtworkID(typ, path); id != "" {
			return &library.ImageRef{URL: "/api/v1/images/" + id, AspectRatio: aspect}
		}
		return nil
	}
	for i := range items {
		id, _ := library.Unwrap(library.ItemPrefix, items[i].ID)
		k := keys[id]
		own, series := art[k], art[seriesKey(k)]
		if own == nil && series == nil {
			continue
		}
		im := items[i].Images
		if im == nil {
			im = &library.Images{}
		}
		poster := own["poster"]
		switch items[i].Kind {
		case "season":
			if poster == "" {
				poster = series["poster"]
			}
		case "episode":
			poster = "" // an episode's picture is its still
			if r := ref("still", own["still"], &wideAspect); r != nil {
				im.Primary = r
			}
		}
		if r := ref("poster", poster, &posterAspect); r != nil {
			im.Primary = r
		}
		backdrop, logo := own["backdrop"], own["logo"]
		if backdrop == "" {
			backdrop = series["backdrop"]
		}
		if logo == "" {
			logo = series["logo"]
		}
		if r := ref("backdrop", backdrop, &wideAspect); r != nil {
			im.Backdrops = []library.ImageRef{*r}
		}
		if r := ref("logo", logo, nil); r != nil {
			im.Logo = r
		}
		items[i].Images = im
	}
	return nil
}

// seriesKey is the key of the series a season's or an episode's key
// belongs to (series/tmdb/1399/s1/e2 → series/tmdb/1399); "" otherwise.
func seriesKey(k string) string {
	if !strings.HasPrefix(k, "series/") {
		return ""
	}
	parts := strings.Split(k, "/")
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[:3], "/")
}

// artworkImage serves a library title's artwork, to profiles that may see
// a title it belongs to, and only at a path Overfin keeps.
func (l Library) artworkImage(w http.ResponseWriter, r *http.Request, p *store.Profile, id, size string) {
	ref, err := images.ParseArtwork(id)
	if err != nil {
		problem.Send(w, problem.NotFound, "")
		return
	}
	data, contentType, err := l.Images.TMDB(r.Context(), ref, size, func(ctx context.Context) error {
		st := l.Provisioner.Store
		ids, err := st.ArtworkItems(ctx, ref.Type, ref.Path, 50)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return images.ErrInvalid
		}
		a, err := l.access(ctx, p)
		if err != nil || a == nil {
			return err
		}
		allowed, _, err := st.AllowedItems(ctx, *a, ids)
		if err != nil {
			return err
		}
		if len(allowed) == 0 {
			return images.ErrHidden
		}
		return nil
	})
	switch {
	case err == nil:
	case errors.Is(err, images.ErrInvalid), errors.Is(err, images.ErrHidden):
		problem.Send(w, problem.NotFound, "")
		return
	case errors.Is(err, images.ErrUnsafe):
		l.Logger.Warn("refused an unsafe image from TMDB", "image", id)
		problem.Send(w, problem.NotFound, "")
		return
	default:
		l.Logger.Error("TMDB image failed", "err", err)
		problem.Send(w, problem.Internal, "")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
