package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
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
// "item/<id>".
func (s *Store) TitleKeys(ctx context.Context, itemIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range itemIDs {
		key, err := s.titleKey(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = key
	}
	return out, nil
}

func (s *Store) titleKey(ctx context.Context, id string) (string, error) {
	var kind, seriesID string
	var tmdb, imdb, tvdb sql.NullString
	var season, episode sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT kind, coalesce(series_id, ''), tmdb, imdb, tvdb, season_number, episode_number
		FROM catalog_items WHERE id = ?`, id).Scan(&kind, &seriesID, &tmdb, &imdb, &tvdb, &season, &episode)
	if errors.Is(err, sql.ErrNoRows) {
		return "item/" + id, nil
	}
	if err != nil {
		return "", err
	}
	switch kind {
	case "movie", "series":
		if k := externalKey(kind, tmdb, imdb, tvdb); k != "" {
			return k, nil
		}
	case "season", "episode":
		if seriesID == "" || !season.Valid || (kind == "episode" && !episode.Valid) {
			break
		}
		var stmdb, simdb, stvdb sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT tmdb, imdb, tvdb FROM catalog_items WHERE id = ? AND kind = 'series'`,
			seriesID).Scan(&stmdb, &simdb, &stvdb)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return "", err
		}
		show := externalKey("series", stmdb, simdb, stvdb)
		if show == "" {
			break
		}
		key := show + "/s" + strconv.FormatInt(season.Int64, 10)
		if kind == "episode" {
			key += "/e" + strconv.FormatInt(episode.Int64, 10)
		}
		return key, nil
	}
	return "item/" + id, nil
}

// externalKey names a film or a show by the first external ID it has.
func externalKey(kind string, tmdb, imdb, tvdb sql.NullString) string {
	switch {
	case tmdb.Valid:
		return kind + "/tmdb/" + tmdb.String
	case kind == "series" && tvdb.Valid:
		return kind + "/tvdb/" + tvdb.String
	case imdb.Valid:
		return kind + "/imdb/" + imdb.String
	}
	return ""
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
