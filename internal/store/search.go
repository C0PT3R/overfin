package store

import (
	"context"
	"strings"
	"time"
)

// Search (POST /search).

// FillSearchNames gives the titles kept before searching existed their
// search name, once.
func (s *Store) FillSearchNames(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tmdb, name FROM catalog_titles WHERE search_name IS NULL`)
	if err != nil {
		return err
	}
	type row struct{ kind, tmdb, name string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.kind, &r.tmdb, &r.name); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(todo) == 0 {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range todo {
		if _, err := tx.ExecContext(ctx, `UPDATE catalog_titles SET search_name = ? WHERE kind = ? AND tmdb = ?`,
			Fold(r.name), r.kind, r.tmdb); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// likeWords is a LIKE pattern for a folded query: its words, in order,
// anything between them.
func likeWords(query string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, w := range strings.Fields(query) {
		b.WriteString(strings.TrimSuffix(likePrefix(w), "%"))
		b.WriteByte('%')
	}
	return b.String()
}

// SearchLibrary finds the library's films and series (by title key) whose
// name matches query: TMDB's name, or a file's own for one TMDB doesn't
// know. Whole names first, then names that start so, then the others,
// the best rated first. At most limit.
func (s *Store) SearchLibrary(ctx context.Context, query string, limit int) ([]string, error) {
	q := Fold(query)
	if q == "" {
		return nil, nil
	}
	pattern, prefix := likeWords(q), strings.TrimSuffix(likePrefix(q), "%")+"%"
	name := `coalesce(t.search_name, lower(i.name))`
	return s.ids(ctx, `SELECT title_key FROM (
			SELECT i.title_key, min(CASE WHEN `+name+` = ? THEN 0 WHEN `+name+` LIKE ? ESCAPE '\' THEN 1 ELSE 2 END) AS rank,
				max(coalesce(t.rating, i.community_rating, 0)) AS rating
			FROM catalog_items i LEFT JOIN catalog_titles t ON t.kind = i.kind AND t.tmdb = i.tmdb
			WHERE i.kind IN ('movie', 'series') AND i.title_key IS NOT NULL AND `+name+` LIKE ? ESCAPE '\'
			GROUP BY i.title_key)
		ORDER BY rank, rating DESC, title_key LIMIT ?`, q, prefix, pattern, limit)
}

// PersonHit is a person a search found.
type PersonHit struct {
	TMDB, Name, Profile, Department string
}

// PutPeopleHits keeps the people a search found, so their photos can be
// shown, without their pages (fetched when opened); those kept already
// stay as they are.
func (s *Store) PutPeopleHits(ctx context.Context, people []PersonHit, at time.Time) error {
	for _, p := range people {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO tmdb_people (id, name, biography, birthplace, department, profile,
				credits, fetched_at) VALUES (?, ?, '', '', ?, ?, '[]', ?) ON CONFLICT (id) DO NOTHING`,
			p.TMDB, p.Name, p.Department, nullString(p.Profile), at.Unix()); err != nil {
			return err
		}
	}
	return nil
}
