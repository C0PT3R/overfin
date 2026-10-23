package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/overfin/server/internal/images"
	"github.com/overfin/server/internal/problem"
	"github.com/overfin/server/internal/store"
)

// Library items' artwork is Overfin's own, from TMDB (catalog.Titles),
// wherever it has some: a title looks the same wherever it's stored.
// Jellyfin's own images stay for what TMDB has none of (titles it doesn't
// know, images placed next to the files).

var (
	posterAspect = 2.0 / 3
	wideAspect   = 16.0 / 9
)

// artworkImage serves a title's artwork, to profiles that may see a title
// it belongs to (in a library or not), and only at a path Overfin keeps.
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
		a, err := l.access(ctx, p)
		if err != nil {
			return err
		}
		// A person's own photo: people carry no rating, library or tag.
		if ref.Type == "profile" {
			if ok, err := st.IsPersonPhoto(ctx, ref.Path); err != nil || ok {
				return err
			}
		}
		if len(ids) > 0 {
			if a == nil {
				return nil
			}
			allowed, _, err := st.AllowedItems(ctx, *a, ids)
			if err != nil || len(allowed) > 0 {
				return err
			}
		}
		// The titles no library has that it's one of.
		keys, err := st.ImageTitles(ctx, ref.Type, ref.Path, 50)
		if err != nil {
			return err
		}
		if len(ids) == 0 && len(keys) == 0 {
			return images.ErrInvalid
		}
		infos, err := st.TitleInfos(ctx, keys, today(), "")
		if err != nil {
			return err
		}
		titles := make([]*store.TitleInfo, 0, len(infos))
		for _, t := range infos {
			titles = append(titles, t)
		}
		visible, err := l.visibleTitles(ctx, p, titles)
		if err != nil {
			return err
		}
		for _, v := range visible {
			if v {
				return nil
			}
		}
		return images.ErrHidden
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
