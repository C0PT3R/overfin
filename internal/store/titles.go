package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Title is a title from TMDB, as the catalog keeps it.
type Title struct {
	Kind     string // movie, series
	TMDB     string
	Name     string
	Year     int
	Overview string
	Poster   string // TMDB image paths, checked by the caller
	Backdrop string
	Status   int // Seerr's media status
	// Details is the title's page (genres, cast, seasons) as JSON; empty
	// until someone opens it.
	Details   []byte
	SeenAt    time.Time
	DetailsAt time.Time // zero without details
	// ItemID is the library item, when the index has the title; empty
	// otherwise.
	ItemID string
	// ReleaseDate (YYYY-MM-DD) and Rating (TMDB's users', 0 to 10, from
	// Votes of them) come with a title's page; "" and 0 until then.
	ReleaseDate string
	Rating      float64
	Votes       int
}

// PutTitles adds or refreshes titles seen at at, keeping their details.
func (s *Store) PutTitles(ctx context.Context, titles []Title, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO catalog_titles
		(kind, tmdb, name, year, overview, poster, backdrop, status, seen_at, sort_name, search_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, tmdb) DO UPDATE SET name = excluded.name, year = excluded.year,
			overview = excluded.overview, poster = excluded.poster, backdrop = excluded.backdrop,
			status = excluded.status, seen_at = excluded.seen_at, sort_name = excluded.sort_name,
			search_name = excluded.search_name`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range titles {
		if _, err := stmt.ExecContext(ctx, t.Kind, t.TMDB, t.Name, nullInt(t.Year), t.Overview,
			nullString(t.Poster), nullString(t.Backdrop), t.Status, at.Unix(), SortName(t.Name), Fold(t.Name)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetTitleDetails stores a title's page, fetched at at; the title itself
// is refreshed with it.
func (s *Store) SetTitleDetails(ctx context.Context, t Title, details []byte, at time.Time) error {
	if err := s.PutTitles(ctx, []Title{t}, at); err != nil {
		return err
	}
	var rating, votes any
	if t.Rating > 0 {
		rating, votes = t.Rating, t.Votes
	}
	_, err := s.db.ExecContext(ctx, `UPDATE catalog_titles SET details = ?, details_at = ?, release_date = ?, rating = ?,
		rating_votes = ? WHERE kind = ? AND tmdb = ?`, string(details), at.Unix(), nullString(t.ReleaseDate), rating, votes,
		t.Kind, t.TMDB)
	return err
}

const titleColumns = `t.kind, t.tmdb, t.name, coalesce(t.year, 0), t.overview, coalesce(t.poster, ''),
	coalesce(t.backdrop, ''), t.status, t.details, t.seen_at, coalesce(t.details_at, 0),
	coalesce((SELECT i.id FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb ORDER BY i.id LIMIT 1), ''),
	coalesce(t.rating, 0), coalesce(t.rating_votes, 0)`

func scanTitle(row interface{ Scan(...any) error }) (*Title, error) {
	var t Title
	var seen, details int64
	if err := row.Scan(&t.Kind, &t.TMDB, &t.Name, &t.Year, &t.Overview, &t.Poster, &t.Backdrop, &t.Status,
		&t.Details, &seen, &details, &t.ItemID, &t.Rating, &t.Votes); err != nil {
		return nil, err
	}
	t.SeenAt = time.Unix(seen, 0)
	if details > 0 {
		t.DetailsAt = time.Unix(details, 0)
	}
	return &t, nil
}

// TitleByTMDB returns a title, or nil when the catalog doesn't have it.
func (s *Store) TitleByTMDB(ctx context.Context, kind, tmdb string) (*Title, error) {
	t, err := scanTitle(s.db.QueryRowContext(ctx, `SELECT `+titleColumns+` FROM catalog_titles t
		WHERE t.kind = ? AND t.tmdb = ?`, kind, tmdb))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// SetList replaces a discovery list with these TMDB IDs, in order.
func (s *Store) SetList(ctx context.Context, list, kind string, tmdbIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_lists WHERE list = ? AND kind = ?`, list, kind); err != nil {
		return err
	}
	for i, id := range tmdbIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_lists (list, kind, position, tmdb) VALUES (?, ?, ?, ?)`,
			list, kind, i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListTitles returns a page of a discovery list, in order, from offset:
// all its titles, those the library has ("library") or those it doesn't
// ("missing"); with a ceiling, only those it allows.
func (s *Store) ListTitles(ctx context.Context, list, kind, availability string, ceiling *Ceiling, offset, limit int) ([]Title, error) {
	filter := ""
	switch availability {
	case "library":
		filter = ` AND EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)`
	case "missing":
		filter = ` AND NOT EXISTS (SELECT 1 FROM catalog_items i WHERE i.kind = t.kind AND i.tmdb = t.tmdb)`
	}
	args := []any{list, kind}
	if ceiling != nil {
		cond, a := ceiling.allowed("t.kind", "t.tmdb")
		filter += ` AND ` + cond
		args = append(args, a...)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+titleColumns+` FROM catalog_lists l
		JOIN catalog_titles t ON t.kind = l.kind AND t.tmdb = l.tmdb
		WHERE l.list = ? AND l.kind = ?`+filter+` ORDER BY l.position LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Title
	for rows.Next() {
		t, err := scanTitle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// PurgeTitles deletes the titles not fetched again since before: TMDB's
// terms allow keeping its data six months at most. Lists drop them too.
func (s *Store) PurgeTitles(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM catalog_titles WHERE seen_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM catalog_lists WHERE NOT EXISTS
		(SELECT 1 FROM catalog_titles t WHERE t.kind = catalog_lists.kind AND t.tmdb = catalog_lists.tmdb)`); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ForgetTMDB deletes every title, list and certification: Overfin no longer uses TMDB's
// data, and its terms ask for it to be deleted then.
func (s *Store) ForgetTMDB(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM catalog_lists`, `DELETE FROM catalog_titles`,
		`DELETE FROM rating_scales`, `DELETE FROM title_ratings`, `DELETE FROM title_ratings_fetched`,
		`DELETE FROM artwork`, `DELETE FROM artwork_fetched`, `DELETE FROM catalog_episodes`, `DELETE FROM catalog_seasons`, `DELETE FROM tmdb_people`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
