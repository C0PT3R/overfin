package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Overfin's own content ratings, from TMDB: each country's scales of
// certifications, each title's certifications, and the rule a kids
// profile is held to (SECURITY.md rule 28).

// RatingLevel is one certification of a country's scale.
type RatingLevel struct {
	Certification string
	Meaning       string
	// Position is TMDB's order: higher is more mature.
	Position int
}

// NotRated is the certification TMDB gives titles no board rated. It is
// never within any ceiling, and never one.
const NotRated = "NR"

// DefaultRatingsCountry is the ratings country until the operator picks one.
const DefaultRatingsCountry = "US"

// RatingsCountry is the country whose certifications apply.
func (s *Store) RatingsCountry(ctx context.Context) (string, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'ratings-country'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultRatingsCountry, nil
	}
	return string(v), err
}

// SetRatingsCountry changes the ratings country. Kids profiles' ceilings
// were on the other country's scales: they're cleared in the same step, so
// those profiles see nothing until their owners pick new ones.
func (s *Store) SetRatingsCountry(ctx context.Context, country string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'ratings-country'`).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) {
		old, err = []byte(DefaultRatingsCountry), nil
	}
	if err != nil {
		return err
	}
	if string(old) == country {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('ratings-country', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, []byte(country)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE profiles SET movie_ceiling = NULL, series_ceiling = NULL
		WHERE kind = 'kids'`); err != nil {
		return err
	}
	return tx.Commit()
}

// SetRatingScales replaces the scales of a kind (movie, series), by
// country.
func (s *Store) SetRatingScales(ctx context.Context, kind string, scales map[string][]RatingLevel) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM rating_scales WHERE kind = ?`, kind); err != nil {
		return err
	}
	for country, levels := range scales {
		for _, l := range levels {
			if _, err := tx.ExecContext(ctx, `INSERT INTO rating_scales (kind, country, certification, meaning, position)
				VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, kind, country, l.Certification, l.Meaning, l.Position); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// RatingScale is a country's scale for a kind, from least to most mature.
func (s *Store) RatingScale(ctx context.Context, kind, country string) ([]RatingLevel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT certification, meaning, position FROM rating_scales
		WHERE kind = ? AND country = ? ORDER BY position, certification`, kind, country)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatingLevel
	for rows.Next() {
		var l RatingLevel
		if err := rows.Scan(&l.Certification, &l.Meaning, &l.Position); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RatingCountries lists the countries with scales for both films and
// series.
func (s *Store) RatingCountries(ctx context.Context) ([]string, error) {
	return s.ids(ctx, `SELECT country FROM rating_scales GROUP BY country
		HAVING count(DISTINCT kind) = 2 ORDER BY country`)
}

// RatedTitle is a title whose certifications Overfin keeps.
type RatedTitle struct {
	Kind string // movie, series
	TMDB string
}

// TitlesToRate returns up to limit titles, in the library or in discovery,
// whose certifications weren't fetched since before.
func (s *Store) TitlesToRate(ctx context.Context, before time.Time, limit int) ([]RatedTitle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tmdb FROM (
			SELECT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL
			UNION SELECT kind, tmdb FROM catalog_titles) t
		WHERE NOT EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = t.kind AND f.tmdb = t.tmdb AND f.fetched_at >= ?)
		LIMIT ?`, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RatedTitle
	for rows.Next() {
		var t RatedTitle
		if err := rows.Scan(&t.Kind, &t.TMDB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTitleRatings replaces a title's certifications, by country (none:
// TMDB has none).
func (s *Store) SetTitleRatings(ctx context.Context, t RatedTitle, ratings map[string][]string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM title_ratings WHERE kind = ? AND tmdb = ?`, t.Kind, t.TMDB); err != nil {
		return err
	}
	for country, certs := range ratings {
		for _, c := range certs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO title_ratings (kind, tmdb, country, certification)
				VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, t.Kind, t.TMDB, country, c); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO title_ratings_fetched (kind, tmdb, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (kind, tmdb) DO UPDATE SET fetched_at = excluded.fetched_at`, t.Kind, t.TMDB, at.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// PurgeRatings deletes the certifications not fetched again since before:
// TMDB's terms allow keeping its data six months at most.
func (s *Store) PurgeRatings(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM title_ratings WHERE EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = title_ratings.kind AND f.tmdb = title_ratings.tmdb AND f.fetched_at < ?)`,
		`DELETE FROM title_ratings_fetched WHERE fetched_at < ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, before.Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RatingProgress counts the library's films and series, and those whose
// certifications were fetched.
func (s *Store) RatingProgress(ctx context.Context) (fetched, total int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(EXISTS (SELECT 1 FROM title_ratings_fetched f
			WHERE f.kind = t.kind AND f.tmdb = t.tmdb)), 0)
		FROM (SELECT DISTINCT kind, tmdb FROM catalog_items WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL) t`).
		Scan(&total, &fetched)
	return fetched, total, err
}

// Ceiling is what a kids profile may see: titles whose every certification
// in Country is within Movie (films) or Series (series, their seasons and
// episodes). An empty ceiling allows nothing.
type Ceiling struct {
	Country string
	Movie   string
	Series  string
}

// allowed is the rule as SQL, for the title kindExpr and tmdbExpr name:
// it has certifications in the country, and none is unrated, unknown to
// the scale, or over the ceiling. Anything missing fails closed.
func (c Ceiling) allowed(kindExpr, tmdbExpr string) (string, []any) {
	cert := `FROM title_ratings r WHERE r.kind = ` + kindExpr + ` AND r.tmdb = ` + tmdbExpr + ` AND r.country = ?`
	return `(` + tmdbExpr + ` IS NOT NULL
		AND EXISTS (SELECT 1 ` + cert + `)
		AND NOT EXISTS (SELECT 1 ` + cert + ` AND (r.certification = ?
			OR coalesce((SELECT l.position FROM rating_scales l
				WHERE l.kind = r.kind AND l.country = r.country AND l.certification = r.certification), 1000000)
			> coalesce((SELECT m.position FROM rating_scales m
				WHERE m.kind = r.kind AND m.country = r.country AND m.certification <> ?
				AND m.certification = CASE r.kind WHEN 'movie' THEN ? ELSE ? END), -1))))`,
		[]any{c.Country, c.Country, NotRated, NotRated, c.Movie, c.Series}
}

// itemAllowed is the rule for the catalog_items row named table: a film
// or a series by its own TMDB ID, a season or an episode by its series'.
func (c Ceiling) itemAllowed(table string) (string, []any) {
	return c.allowed(`(CASE `+table+`.kind WHEN 'movie' THEN 'movie' ELSE 'series' END)`,
		`(CASE WHEN `+table+`.kind IN ('movie', 'series') THEN `+table+`.tmdb
			ELSE (SELECT ser.tmdb FROM catalog_items ser WHERE ser.id = `+table+`.series_id AND ser.kind = 'series') END)`)
}

// TitleAllowed says whether the ceiling allows a TMDB title (movie,
// series).
func (s *Store) TitleAllowed(ctx context.Context, c Ceiling, kind, tmdb string) (bool, error) {
	cond, args := c.allowed("t.kind", "t.tmdb")
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM (SELECT ? AS kind, ? AS tmdb) t WHERE `+cond+`)`,
		append([]any{kind, tmdb}, args...)...).Scan(&ok)
	return ok, err
}

// KidsProfiles lists every kids profile.
func (s *Store) KidsProfiles(ctx context.Context) ([]*Profile, error) {
	rows, err := s.db.QueryContext(ctx, profileSelect+` WHERE kind = 'kids'`)
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
