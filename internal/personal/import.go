// Package personal brings what each profile did in Jellyfin (what it
// watched, where it stopped, how many times, its favorites) over to
// Overfin's database once, so Overfin keeps it from then on, for every
// title wherever it plays.
//
// It reads with each profile's own token, never the admin key: Jellyfin
// gives a profile its own data and nothing else.
package personal

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/C0PT3R/overfin-server/internal/jellyfin"
	"github.com/C0PT3R/overfin-server/internal/provision"
	"github.com/C0PT3R/overfin-server/internal/store"
)

// Importer brings profiles' data over, each once, as profiles appear
// (accounts brought over by migration included).
type Importer struct {
	Jellyfin *jellyfin.Client
	Store    *store.Store
	Logger   *slog.Logger
	// Every is how often it looks for profiles not imported yet.
	Every time.Duration
}

// Run imports until ctx ends. It waits for the catalog's index, which
// names the titles.
func (im *Importer) Run(ctx context.Context) {
	for {
		if err := im.Once(ctx); err != nil && ctx.Err() == nil {
			im.Logger.Error("personal: importing from Jellyfin failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(im.Every):
		}
	}
}

// Once imports every profile not imported yet.
func (im *Importer) Once(ctx context.Context) error {
	st, err := im.Store.CatalogState(ctx)
	if err != nil || st.Pass == 0 {
		return err // the index isn't ready: next time
	}
	profiles, err := im.Store.ProfilesToImport(ctx)
	if err != nil {
		return err
	}
	for _, p := range profiles {
		n, err := im.profile(ctx, p)
		if jellyfin.IsStatus(err, http.StatusUnauthorized) {
			continue // its token is being renewed: next time
		}
		if err != nil {
			return err
		}
		if err := im.Store.MarkImported(ctx, p.ID, time.Now()); err != nil {
			return err
		}
		im.Logger.Info("personal: a profile's data was brought over from Jellyfin", "profile", p.ID, "titles", n)
	}
	return nil
}

// pageSize is how many items one request reads.
const pageSize = 500

// profile imports one profile: what it watched, what it's in the middle
// of, and its favorites.
func (im *Importer) profile(ctx context.Context, p *store.Profile) (int, error) {
	a := jellyfin.Auth{Token: p.JellyfinToken, DeviceID: provision.DeviceID(p.JellyfinUserID)}
	byItem := map[string]jellyfin.UserData{}
	played, favorite := true, true
	for _, q := range []jellyfin.ItemQuery{
		{Recursive: true, Types: []string{"Movie", "Episode", "Video", "MusicVideo"}, IsPlayed: &played},
		{Recursive: true, Resumable: true},
		{Recursive: true, IsFavorite: &favorite},
	} {
		for start := 0; ; start += pageSize {
			q.StartIndex, q.Limit = start, pageSize
			page, err := im.Jellyfin.Items(ctx, a, p.JellyfinUserID, q)
			if err != nil {
				return 0, err
			}
			for _, it := range page.Items {
				id, err := jellyfin.NormalizeID(it.Id)
				if err != nil || it.UserData == nil {
					continue
				}
				byItem[id] = *it.UserData
			}
			if len(page.Items) < pageSize {
				break
			}
		}
	}
	if len(byItem) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(byItem))
	for id := range byItem {
		ids = append(ids, id)
	}
	keys, err := im.Store.TitleKeys(ctx, ids)
	if err != nil {
		return 0, err
	}
	states := make([]store.Personal, 0, len(byItem))
	for id, u := range byItem {
		s := store.Personal{Title: keys[id], ItemID: id, Played: u.Played, PlayCount: u.PlayCount, Favorite: u.IsFavorite}
		if u.PlaybackPositionTicks > 0 {
			s.PositionMs = u.PlaybackPositionTicks / 10_000
		}
		if t, err := time.Parse(time.RFC3339Nano, u.LastPlayedDate); err == nil {
			s.LastPlayedAt = t
		}
		states = append(states, s)
	}
	return len(states), im.Store.MergePersonal(ctx, p.ID, states)
}
