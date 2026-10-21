package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Personal is what a profile has done with a title.
type Personal struct {
	// Title is the title's key (TitleKeys); ItemID, the library item it was
	// last seen as.
	Title        string
	ItemID       string
	PositionMs   int64
	Played       bool
	PlayCount    int
	LastPlayedAt time.Time // zero: never
	Favorite     bool
}

// TitleKeys gives library items their title's key, which names the title
// itself rather than one of its copies: "movie/tmdb/949",
// "series/tmdb/1399/s1/e2". An item the index can't name that way (no
// external ID, no numbers, or not indexed) keeps a key of its own,
// "item/<id>". Keys are computed in the index (RefreshTitleKeys).
func (s *Store) TitleKeys(ctx context.Context, itemIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range itemIDs {
		var key sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT title_key FROM catalog_items WHERE id = ?`, id).Scan(&key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		out[id] = key.String
		if !key.Valid {
			out[id] = "item/" + id
		}
	}
	return out, nil
}

// TitleKeysReady reports whether every indexed item has its title key
// (RefreshTitleKeys ran since the last items arrived).
func (s *Store) TitleKeysReady(ctx context.Context) (bool, error) {
	var missing int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM catalog_items WHERE title_key IS NULL`).Scan(&missing)
	return missing == 0, err
}

// showKey is a show's key from its external IDs, in SQL: TMDB, else TVDB,
// else IMDb; NULL without any.
const showKey = `CASE WHEN s.tmdb IS NOT NULL THEN 'series/tmdb/' || s.tmdb
	WHEN s.tvdb IS NOT NULL THEN 'series/tvdb/' || s.tvdb
	WHEN s.imdb IS NOT NULL THEN 'series/imdb/' || s.imdb END`

// RefreshTitleKeys computes every indexed item's title key: a film or a
// show by the first external ID it has (TMDB, then TVDB for shows, then
// IMDb), a season or an episode by its show's key and its numbers, and any
// item that can't be named so by its own ID. Anything missing (an ID, a
// number) makes the whole key NULL, which falls back to the item's own.
func (s *Store) RefreshTitleKeys(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_items SET title_key = coalesce(CASE
		WHEN kind = 'movie' THEN CASE WHEN tmdb IS NOT NULL THEN 'movie/tmdb/' || tmdb
			WHEN imdb IS NOT NULL THEN 'movie/imdb/' || imdb END
		WHEN kind = 'series' THEN (SELECT `+showKey+` FROM catalog_items s WHERE s.id = catalog_items.id)
		WHEN kind = 'season' THEN (SELECT `+showKey+` FROM catalog_items s
			WHERE s.id = catalog_items.series_id AND s.kind = 'series') || '/s' || season_number
		WHEN kind = 'episode' THEN (SELECT `+showKey+` FROM catalog_items s
			WHERE s.id = catalog_items.series_id AND s.kind = 'series') || '/s' || season_number || '/e' || episode_number
		END, 'item/' || id)`)
	return err
}

// MergePersonal adds what a profile did with titles to what's kept, the
// way two copies of one title add up: watched if either is, the furthest
// position, the most plays, the latest play, a favorite if either is.
func (s *Store) MergePersonal(ctx context.Context, profileID string, states []Personal) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO profile_titles (profile_id, title, item_id, position_ms,
		played, play_count, last_played_at, favorite, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, title) DO UPDATE SET
			item_id = coalesce(excluded.item_id, item_id),
			position_ms = max(position_ms, excluded.position_ms),
			played = max(played, excluded.played),
			play_count = max(play_count, excluded.play_count),
			last_played_at = max(coalesce(last_played_at, 0), coalesce(excluded.last_played_at, 0)),
			favorite = max(favorite, excluded.favorite),
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().Unix()
	for _, p := range states {
		var last any
		if !p.LastPlayedAt.IsZero() {
			last = p.LastPlayedAt.Unix()
		}
		if _, err := stmt.ExecContext(ctx, profileID, p.Title, nullString(p.ItemID), p.PositionMs,
			boolInt(p.Played), p.PlayCount, last, boolInt(p.Favorite), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PersonalStates returns what a profile did with these titles, by title
// key; titles it never touched are left out.
func (s *Store) PersonalStates(ctx context.Context, profileID string, titles []string) (map[string]Personal, error) {
	out := map[string]Personal{}
	for _, title := range titles {
		var p Personal
		var item sql.NullString
		var played, favorite int
		var last sql.NullInt64
		err := s.db.QueryRowContext(ctx, `SELECT title, item_id, position_ms, played, play_count, last_played_at, favorite
			FROM profile_titles WHERE profile_id = ? AND title = ?`, profileID, title).
			Scan(&p.Title, &item, &p.PositionMs, &played, &p.PlayCount, &last, &favorite)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		p.ItemID, p.Played, p.Favorite = item.String, played == 1, favorite == 1
		if last.Valid && last.Int64 > 0 {
			p.LastPlayedAt = time.Unix(last.Int64, 0)
		}
		out[title] = p
	}
	return out, nil
}

// ProfilesToImport lists the profiles whose data hasn't been brought over
// from Jellyfin yet.
func (s *Store) ProfilesToImport(ctx context.Context) ([]*Profile, error) {
	rows, err := s.db.QueryContext(ctx, profileSelect+` WHERE id NOT IN (SELECT profile_id FROM profile_imports)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Profile
	for rows.Next() {
		p, err := s.scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkImported records that a profile's data was brought over.
func (s *Store) MarkImported(ctx context.Context, profileID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO profile_imports (profile_id, imported_at) VALUES (?, ?)`,
		profileID, at.Unix())
	return err
}

// AllPersonal returns everything kept about a profile's titles, for its
// data download.
func (s *Store) AllPersonal(ctx context.Context, profileID string) ([]Personal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT title, coalesce(item_id, ''), position_ms, played, play_count,
		coalesce(last_played_at, 0), favorite FROM profile_titles WHERE profile_id = ? ORDER BY title`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Personal{}
	for rows.Next() {
		var p Personal
		var played, favorite int
		var last int64
		if err := rows.Scan(&p.Title, &p.ItemID, &p.PositionMs, &played, &p.PlayCount, &last, &favorite); err != nil {
			return nil, err
		}
		p.Played, p.Favorite = played == 1, favorite == 1
		if last > 0 {
			p.LastPlayedAt = time.Unix(last, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPlay records where a profile is in a title: at positionMs, or
// finished (watched once more: its position goes back to the start).
func (s *Store) RecordPlay(ctx context.Context, profileID, title, itemID string, positionMs int64, finished bool, at time.Time) error {
	if finished {
		_, err := s.db.ExecContext(ctx, `INSERT INTO profile_titles (profile_id, title, item_id, position_ms,
			played, play_count, last_played_at, updated_at) VALUES (?, ?, ?, 0, 1, 1, ?, ?)
			ON CONFLICT (profile_id, title) DO UPDATE SET item_id = excluded.item_id, position_ms = 0, played = 1,
				play_count = play_count + 1, last_played_at = excluded.last_played_at, updated_at = excluded.updated_at`,
			profileID, title, itemID, at.Unix(), at.Unix())
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO profile_titles (profile_id, title, item_id, position_ms,
		last_played_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, title) DO UPDATE SET item_id = excluded.item_id, position_ms = excluded.position_ms,
			last_played_at = excluded.last_played_at, updated_at = excluded.updated_at`,
		profileID, title, itemID, positionMs, at.Unix(), at.Unix())
	return err
}

// SetPlayed marks titles (title key → item ID) watched or not. Watched, a
// title's position goes back to the start and it counts as played at
// least once; not watched, its position and count start over.
func (s *Store) SetPlayed(ctx context.Context, profileID string, titles map[string]string, played bool, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for title, itemID := range titles {
		var q string
		if played {
			q = `INSERT INTO profile_titles (profile_id, title, item_id, played, play_count, last_played_at, updated_at)
				VALUES (?, ?, ?, 1, 1, ?, ?)
				ON CONFLICT (profile_id, title) DO UPDATE SET item_id = excluded.item_id, played = 1, position_ms = 0,
					play_count = max(play_count, 1), last_played_at = excluded.last_played_at, updated_at = excluded.updated_at`
		} else {
			q = `INSERT INTO profile_titles (profile_id, title, item_id, last_played_at, updated_at) VALUES (?, ?, ?, NULL, ?)
				ON CONFLICT (profile_id, title) DO UPDATE SET item_id = excluded.item_id, played = 0, position_ms = 0,
					play_count = 0, updated_at = excluded.updated_at`
		}
		args := []any{profileID, title, itemID, at.Unix(), at.Unix()}
		if !played {
			args = []any{profileID, title, itemID, at.Unix()}
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetFavorite marks a title a favorite of the profile, or not.
func (s *Store) SetFavorite(ctx context.Context, profileID, title, itemID string, favorite bool, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO profile_titles (profile_id, title, item_id, favorite, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (profile_id, title) DO UPDATE SET item_id = excluded.item_id, favorite = excluded.favorite,
			updated_at = excluded.updated_at`, profileID, title, itemID, boolInt(favorite), at.Unix())
	return err
}

// CatalogEpisodeIDs lists the episodes the index has under a series or a
// season.
func (s *Store) CatalogEpisodeIDs(ctx context.Context, parentID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM catalog_items WHERE kind = 'episode'
		AND (series_id = ? OR season_id = ?)`, parentID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Imported reports whether a profile's data was brought over from Jellyfin.
func (s *Store) Imported(ctx context.Context, profileID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM profile_imports WHERE profile_id = ?`, profileID).Scan(&n)
	return n > 0, err
}

// InProgress lists the items a profile is in the middle of, the last
// played first: Continue Watching. A title watched before and started
// again is in the middle too. kinds, when set, keeps those the index knows
// as one of them.
func (s *Store) InProgress(ctx context.Context, profileID string, kinds []string, offset, limit int) ([]string, error) {
	q := `SELECT pt.item_id FROM profile_titles pt LEFT JOIN catalog_items ci ON ci.id = pt.item_id
		WHERE pt.profile_id = ? AND pt.position_ms > 0 AND pt.item_id IS NOT NULL`
	args := []any{profileID}
	if len(kinds) > 0 {
		q += ` AND ci.kind IN (` + marks(len(kinds)) + `)`
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	q += ` ORDER BY pt.last_played_at DESC, pt.title LIMIT ? OFFSET ?`
	return s.ids(ctx, q, append(args, limit, offset)...)
}

// NextUp lists, for each series a profile watched an episode of, the next
// episode the library has that it hasn't watched, the series watched last
// first. seriesID, when set, keeps that series only. Specials (season 0)
// come next only after a special.
func (s *Store) NextUp(ctx context.Context, profileID, seriesID string, offset, limit int) ([]string, error) {
	filter := ""
	args := []any{profileID, profileID}
	if seriesID != "" {
		filter = ` AND series = ?`
		args = append(args, seriesID)
	}
	q := `WITH watched AS (
			SELECT e.series_id AS series, e.season_number AS sn, e.episode_number AS en, pt.last_played_at AS at,
				row_number() OVER (PARTITION BY e.series_id
					ORDER BY pt.last_played_at DESC, e.season_number DESC, e.episode_number DESC) AS r
			FROM profile_titles pt
			JOIN catalog_items e ON e.title_key = pt.title AND e.kind = 'episode'
			WHERE pt.profile_id = ? AND pt.played = 1 AND e.series_id IS NOT NULL
				AND e.season_number IS NOT NULL AND e.episode_number IS NOT NULL
		), next AS (
			SELECT w.series, w.at, (SELECT n.id FROM catalog_items n
				WHERE n.kind = 'episode' AND n.series_id = w.series
					AND (n.season_number > w.sn OR (n.season_number = w.sn AND n.episode_number > w.en))
					AND (n.season_number > 0 OR w.sn = 0)
					AND NOT EXISTS (SELECT 1 FROM profile_titles x
						WHERE x.profile_id = ? AND x.title = n.title_key AND x.played = 1)
				ORDER BY n.season_number, n.episode_number, n.id LIMIT 1) AS id
			FROM watched w WHERE w.r = 1
		)
		SELECT id FROM next WHERE id IS NOT NULL` + filter + ` ORDER BY at DESC, id LIMIT ? OFFSET ?`
	return s.ids(ctx, q, append(args, limit, offset)...)
}

func (s *Store) ids(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func marks(n int) string {
	if n <= 0 {
		return ""
	}
	return "?" + strings.Repeat(", ?", n-1)
}

// ForgetProgress forgets where a profile stopped in a title, which takes it
// out of Continue Watching; whether it watched it is kept.
func (s *Store) ForgetProgress(ctx context.Context, profileID, title string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE profile_titles SET position_ms = 0, updated_at = ?
		WHERE profile_id = ? AND title = ?`, time.Now().Unix(), profileID, title)
	return err
}

// ClearPersonal forgets everything kept about a profile's titles.
func (s *Store) ClearPersonal(ctx context.Context, profileID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM profile_titles WHERE profile_id = ?`, profileID)
	return err
}
