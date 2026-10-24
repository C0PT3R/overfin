package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// SeriesUpNext is the episode playing a series starts, for a profile: the
// one it's in the middle of (and where), else the one after the last it
// finished, else the first; among the episodes the libraries have, in
// order, specials last; with a, only those it allows. "" when the
// libraries have none.
func (s *Store) SeriesUpNext(ctx context.Context, profileID string, series TitleRef, a *Access) (key string, positionMs int64, err error) {
	lo, hi := keyRange(series.Key() + "/s")
	allowed, args := "1 = 1", []any(nil)
	if a != nil {
		allowed, args = a.itemAllowed("catalog_items")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT title_key, season_number, episode_number FROM catalog_items
		WHERE kind = 'episode' AND title_key >= ? AND title_key < ? AND season_number IS NOT NULL AND episode_number IS NOT NULL
		AND `+allowed+`
		ORDER BY season_number = 0, season_number, episode_number`, append([]any{lo, hi}, args...)...)
	if err != nil {
		return "", 0, err
	}
	var episodes []string
	at := map[string]int{}
	for rows.Next() {
		var k string
		var sn, en int
		if err := rows.Scan(&k, &sn, &en); err != nil {
			rows.Close()
			return "", 0, err
		}
		if _, seen := at[k]; !seen {
			at[k] = len(episodes)
			episodes = append(episodes, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(episodes) == 0 {
		return "", 0, err
	}
	// The one in the middle, the latest first.
	var title string
	var pos int64
	err = s.db.QueryRowContext(ctx, `SELECT title, position_ms FROM profile_titles
		WHERE profile_id = ? AND title >= ? AND title < ? AND position_ms > 0 AND played = 0
		ORDER BY last_played_at DESC LIMIT 1`, profileID, lo, hi).Scan(&title, &pos)
	if err == nil {
		if _, ok := at[title]; ok {
			return title, pos, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	// The one after the last finished; the first after the end.
	err = s.db.QueryRowContext(ctx, `SELECT title FROM profile_titles
		WHERE profile_id = ? AND title >= ? AND title < ? AND played = 1
		ORDER BY coalesce(last_played_at, 0) DESC, updated_at DESC LIMIT 1`, profileID, lo, hi).Scan(&title)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", 0, err
	default:
		// Specials come next only after a special.
		if i, ok := at[title]; ok && i+1 < len(episodes) && (isSpecial(title) || !isSpecial(episodes[i+1])) {
			return episodes[i+1], 0, nil
		}
	}
	return episodes[0], 0, nil
}

// DismissContinue takes a series out of a profile's Continue Watching: its
// next episode stays out until the profile watches the series again.
func (s *Store) DismissContinue(ctx context.Context, profileID string, series TitleRef, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO continue_dismissed (profile_id, series, dismissed_at) VALUES (?, ?, ?)
		ON CONFLICT (profile_id, series) DO UPDATE SET dismissed_at = excluded.dismissed_at`, profileID, series.Key(), at.Unix())
	return err
}

// dismissed are the series a profile took out of Continue Watching, and
// when.
func (s *Store) dismissed(ctx context.Context, profileID string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT series, dismissed_at FROM continue_dismissed WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var at int64
		if err := rows.Scan(&k, &at); err != nil {
			return nil, err
		}
		out[k] = at
	}
	return out, rows.Err()
}

func isSpecial(key string) bool {
	r, ok := ParseTitleKey(key)
	return ok && r.Season == 0
}

// DismissedSeries lists, for the data download, the series a profile took
// out of Continue Watching, by title key, with when.
func (s *Store) DismissedSeries(ctx context.Context, profileID string) (map[string]time.Time, error) {
	d, err := s.dismissed(ctx, profileID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(d))
	for k, at := range d {
		out[k] = time.Unix(at, 0).UTC()
	}
	return out, nil
}
