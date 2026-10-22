package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Titles an admin identified by hand (migration 030): the TMDB title a
// film or a series really is, over the one Jellyfin gave it. Overfin keeps
// the choice; Jellyfin isn't told, and its scans can't undo it.

// SetOverride makes a film or a series (by Jellyfin item ID) the TMDB
// title tmdb. What profiles did with it (watched, where they stopped, the
// watchlist) moves to the title it really is, unless another copy is still
// the old title: then it's that one's. Requests stay: they say what Seerr
// was asked for. ErrNotFound when the item isn't a film or a series of the
// index.
func (s *Store) SetOverride(ctx context.Context, itemID, tmdb string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	var oldKey sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT kind, title_key FROM catalog_items WHERE id = ? AND kind IN ('movie', 'series')`,
		itemID).Scan(&kind, &oldKey)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_overrides (item_id, tmdb, set_at) VALUES (?, ?, ?)
		ON CONFLICT (item_id) DO UPDATE SET tmdb = excluded.tmdb, set_at = excluded.set_at`, itemID, tmdb, at.Unix()); err != nil {
		return err
	}
	if err := s.retitle(ctx, tx, itemID, kind, tmdb, oldKey.String); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTitleKeys(ctx)
}

// ClearOverride gives a film or a series back the TMDB title Jellyfin
// gave it; what profiles did with it moves back the same way.
func (s *Store) ClearOverride(ctx context.Context, itemID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind string
	var oldKey, jellyfinTMDB sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT kind, title_key, jellyfin_tmdb FROM catalog_items
		WHERE id = ? AND kind IN ('movie', 'series')`, itemID).Scan(&kind, &oldKey, &jellyfinTMDB)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_overrides WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	if err := s.retitle(ctx, tx, itemID, kind, jellyfinTMDB.String, oldKey.String); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTitleKeys(ctx)
}

// retitle sets an item's TMDB ID ("" for none) and moves what profiles did
// with its old title (oldKey, and its seasons and episodes) to the new
// one, unless another film or series is still the old title.
func (s *Store) retitle(ctx context.Context, tx *sql.Tx, itemID, kind, tmdb, oldKey string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_items SET tmdb = ? WHERE id = ?`, nullString(tmdb), itemID); err != nil {
		return err
	}
	if tmdb == "" || oldKey == "" {
		return nil
	}
	newKey := kind + "/tmdb/" + tmdb
	if newKey == oldKey {
		return nil
	}
	var shared bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_items WHERE title_key = ? AND id != ?
		AND kind IN ('movie', 'series'))`, oldKey, itemID).Scan(&shared); err != nil || shared {
		return err
	}
	lo, hi := keyRange(oldKey + "/")
	_, err := tx.ExecContext(ctx, `UPDATE OR IGNORE profile_titles SET title = ? || substr(title, ?)
		WHERE title = ? OR (title >= ? AND title < ?)`, newKey, len(oldKey)+1, oldKey, lo, hi)
	return err
}

// Overrides returns the TMDB IDs admins chose, by item ID.
func (s *Store) Overrides(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, tmdb FROM title_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, tmdb string
		if err := rows.Scan(&id, &tmdb); err != nil {
			return nil, err
		}
		out[id] = tmdb
	}
	return out, rows.Err()
}
