package personal

import (
	"context"
	"regexp"
	"strconv"
	"time"

	"github.com/overfin/server/internal/jellyfin"
	"github.com/overfin/server/internal/store"
)

// FromServer reads what a user did on another Jellyfin server (what they
// watched, where they stopped, their favorites), named by title keys so it
// joins with this server's titles whatever their item IDs: a film by its
// TMDB or IMDb ID, a show by its TMDB, TVDB or IMDb ID, an episode by its
// show's and its numbers (store.TitleKeys). What can't be named that way
// is left out; skipped counts it.
func FromServer(ctx context.Context, jf *jellyfin.Client, a jellyfin.Auth, userID string) (states []store.Personal, skipped int, err error) {
	items := map[string]jellyfin.RawItem{}
	played, favorite := true, true
	for _, q := range []jellyfin.ItemQuery{
		{Recursive: true, Types: []string{"Movie", "Episode"}, IsPlayed: &played},
		{Recursive: true, Types: []string{"Movie", "Episode"}, Resumable: true},
		{Recursive: true, Types: []string{"Movie", "Episode", "Series"}, IsFavorite: &favorite},
	} {
		q.ExtraFields = "ProviderIds"
		for start := 0; ; start += pageSize {
			q.StartIndex, q.Limit = start, pageSize
			page, err := jf.Items(ctx, a, userID, q)
			if err != nil {
				return nil, 0, err
			}
			for _, it := range page.Items {
				if id, err := jellyfin.NormalizeID(it.Id); err == nil && it.UserData != nil {
					items[id] = it
				}
			}
			if len(page.Items) < pageSize {
				break
			}
		}
	}

	// Episodes are named by their show: its IDs, read in batches.
	shows := map[string]string{} // show item ID → its key
	var wanted []string
	for _, it := range items {
		switch it.Type {
		case "Series":
			if id, err := jellyfin.NormalizeID(it.Id); err == nil {
				shows[id] = showKey(it.ProviderIds)
			}
		case "Episode":
			if id, err := jellyfin.NormalizeID(it.SeriesId); err == nil {
				wanted = append(wanted, id)
			}
		}
	}
	for len(wanted) > 0 {
		batch := wanted[:min(100, len(wanted))]
		wanted = wanted[len(batch):]
		var ask []string
		for _, id := range batch {
			if _, ok := shows[id]; !ok {
				ask = append(ask, id)
				shows[id] = ""
			}
		}
		if len(ask) == 0 {
			continue
		}
		page, err := jf.Items(ctx, a, userID, jellyfin.ItemQuery{IDs: ask, ExtraFields: "ProviderIds", Limit: len(ask)})
		if err != nil {
			return nil, 0, err
		}
		for _, s := range page.Items {
			if id, err := jellyfin.NormalizeID(s.Id); err == nil {
				shows[id] = showKey(s.ProviderIds)
			}
		}
	}

	for _, it := range items {
		key := ""
		switch it.Type {
		case "Movie":
			key = movieKey(it.ProviderIds)
		case "Series":
			key = showKey(it.ProviderIds)
		case "Episode":
			if id, err := jellyfin.NormalizeID(it.SeriesId); err == nil && shows[id] != "" &&
				it.ParentIndexNumber != nil && it.IndexNumber != nil {
				key = shows[id] + "/s" + strconv.Itoa(*it.ParentIndexNumber) + "/e" + strconv.Itoa(*it.IndexNumber)
			}
		}
		if key == "" {
			skipped++
			continue
		}
		states = append(states, stateOf(key, "", *it.UserData))
	}
	return states, skipped, nil
}

// stateOf is what UserData says a profile did with a title.
func stateOf(key, itemID string, u jellyfin.UserData) store.Personal {
	s := store.Personal{Title: key, ItemID: itemID, Played: u.Played, PlayCount: u.PlayCount, Favorite: u.IsFavorite}
	if u.PlaybackPositionTicks > 0 {
		s.PositionMs = u.PlaybackPositionTicks / 10_000
	}
	if t, err := time.Parse(time.RFC3339Nano, u.LastPlayedDate); err == nil {
		s.LastPlayedAt = t
	}
	return s
}

// External IDs as they're checked before going into a key (SECURITY.md
// rule 7).
var (
	numberID = regexp.MustCompile(`^[0-9]{1,12}$`)
	imdbID   = regexp.MustCompile(`^tt[0-9]{1,12}$`)
)

// movieKey names a film by its TMDB ID, else its IMDb ID; "" with neither.
func movieKey(ids map[string]string) string {
	switch {
	case numberID.MatchString(ids["Tmdb"]):
		return "movie/tmdb/" + ids["Tmdb"]
	case imdbID.MatchString(ids["Imdb"]):
		return "movie/imdb/" + ids["Imdb"]
	}
	return ""
}

// showKey names a show by its TMDB ID, else TVDB, else IMDb; "" with none.
func showKey(ids map[string]string) string {
	switch {
	case numberID.MatchString(ids["Tmdb"]):
		return "series/tmdb/" + ids["Tmdb"]
	case numberID.MatchString(ids["Tvdb"]):
		return "series/tvdb/" + ids["Tvdb"]
	case imdbID.MatchString(ids["Imdb"]):
		return "series/imdb/" + ids["Imdb"]
	}
	return ""
}
