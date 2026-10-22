package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// Overfin's own artwork, from TMDB, by title key.

// ArtworkTarget is a film or a series whose artwork is to be fetched, with
// the seasons the library has of it.
type ArtworkTarget struct {
	Kind    string // movie, series
	TMDB    string
	Seasons []int
}

// Key is the target's title key.
func (t ArtworkTarget) Key() string { return t.Kind + "/tmdb/" + t.TMDB }

// ArtworkToFetch returns up to limit films and series of the library whose
// artwork wasn't fetched since before.
func (s *Store) ArtworkToFetch(ctx context.Context, before time.Time, limit int) ([]ArtworkTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tmdb, (SELECT group_concat(DISTINCT se.season_number)
			FROM catalog_items se JOIN catalog_items o ON o.id = se.series_id
			WHERE t.kind = 'series' AND se.kind = 'season' AND se.season_number IS NOT NULL
			AND o.kind = 'series' AND o.tmdb = t.tmdb)
		FROM (SELECT DISTINCT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL) t
		WHERE NOT EXISTS (SELECT 1 FROM artwork_fetched f WHERE f.key = t.kind || '/tmdb/' || t.tmdb AND f.fetched_at >= ?)
		LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtworkTarget
	for rows.Next() {
		var t ArtworkTarget
		var seasons sql.NullString
		if err := rows.Scan(&t.Kind, &t.TMDB, &seasons); err != nil {
			return nil, err
		}
		for _, n := range splitList(seasons.String) {
			if v, err := strconv.Atoi(n); err == nil {
				t.Seasons = append(t.Seasons, v)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func splitList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// SetArtwork replaces a target's artwork, its seasons' and episodes'
// included: images maps title keys to their images by type ("poster",
// "backdrop", "logo", "still").
func (s *Store) SetArtwork(ctx context.Context, t ArtworkTarget, images map[string]map[string]string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := t.Key()
	if _, err := tx.ExecContext(ctx, `DELETE FROM artwork WHERE key = ? OR key LIKE ? ESCAPE '\'`,
		key, likePrefix(key+"/")); err != nil {
		return err
	}
	for k, byType := range images {
		if k != key && !hasPrefix(k, key+"/") {
			continue // only the target's own
		}
		for typ, path := range byType {
			if path == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO artwork (key, type, path) VALUES (?, ?, ?)
				ON CONFLICT (key, type) DO UPDATE SET path = excluded.path`, k, typ, path); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artwork_fetched (key, fetched_at) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET fetched_at = excluded.fetched_at`, key, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// likePrefix is a LIKE pattern for what starts with p.
func likePrefix(p string) string {
	out := make([]byte, 0, len(p)+1)
	for i := 0; i < len(p); i++ {
		if p[i] == '%' || p[i] == '_' || p[i] == '\\' {
			out = append(out, '\\')
		}
		out = append(out, p[i])
	}
	return string(append(out, '%'))
}

// Artwork returns the artwork of title keys: key → type → TMDB path.
func (s *Store) Artwork(ctx context.Context, keys []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for start := 0; start < len(keys); start += 500 {
		chunk := keys[start:min(start+500, len(keys))]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `SELECT key, type, path FROM artwork WHERE key IN (`+marks(len(chunk))+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k, typ, path string
			if err := rows.Scan(&k, &typ, &path); err != nil {
				rows.Close()
				return nil, err
			}
			if out[k] == nil {
				out[k] = map[string]string{}
			}
			out[k][typ] = path
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ArtworkItems lists library items (by Jellyfin ID) the image at path
// (of type typ) belongs to: the titles with that key, and for a series'
// own image, its seasons and episodes too. At most limit.
func (s *Store) ArtworkItems(ctx context.Context, typ, path string, limit int) ([]string, error) {
	return s.ids(ctx, `SELECT i.id FROM artwork a JOIN catalog_items i
			ON i.title_key = a.key OR i.title_key LIKE replace(replace(a.key, '%', '\%'), '_', '\_') || '/%' ESCAPE '\'
		WHERE a.type = ? AND a.path = ? LIMIT ?`, typ, path, limit)
}

// PurgeArtwork deletes the artwork not fetched again since before.
func (s *Store) PurgeArtwork(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keys, err := tx.QueryContext(ctx, `SELECT key FROM artwork_fetched WHERE fetched_at < ?`, before.Unix())
	if err != nil {
		return err
	}
	var old []string
	for keys.Next() {
		var k string
		if err := keys.Scan(&k); err != nil {
			keys.Close()
			return err
		}
		old = append(old, k)
	}
	keys.Close()
	for _, k := range old {
		if _, err := tx.ExecContext(ctx, `DELETE FROM artwork WHERE key = ? OR key LIKE ? ESCAPE '\'`, k, likePrefix(k+"/")); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM artwork_fetched WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TMDBLanguage is the language TMDB's data is asked in (ISO 639-1).
func (s *Store) TMDBLanguage(ctx context.Context) (string, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'tmdb-language'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "en", nil
	}
	return string(v), err
}

// SetTMDBLanguage changes it. Artwork is fetched again in the new language,
// and so are titles' pages.
func (s *Store) SetTMDBLanguage(ctx context.Context, lang string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('tmdb-language', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(lang)); err != nil {
		return err
	}
	// Fetched again: artwork at the keeper's next pass, a title's page the
	// next time it's opened.
	for _, q := range []string{`DELETE FROM artwork_fetched`, `UPDATE catalog_titles SET details_at = 0`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
