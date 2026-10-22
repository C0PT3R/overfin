package store

import (
	"context"
	"encoding/json"
)

// Library is a library as the index last read it: its ID, and Jellyfin's
// view of it, as JSON.
type Library struct {
	ID   string
	Item json.RawMessage
}

// SetLibraries replaces the libraries the index knows, in order.
func (s *Store) SetLibraries(ctx context.Context, libs []Library) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_libraries`); err != nil {
		return err
	}
	for i, l := range libs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_libraries (id, position, item) VALUES (?, ?, ?)`,
			l.ID, i, string(l.Item)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Libraries returns the libraries the index knows, in order; none before
// its first read.
func (s *Store) Libraries(ctx context.Context) ([]Library, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, item FROM catalog_libraries ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		var item string
		if err := rows.Scan(&l.ID, &item); err != nil {
			return nil, err
		}
		l.Item = json.RawMessage(item)
		out = append(out, l)
	}
	return out, rows.Err()
}
