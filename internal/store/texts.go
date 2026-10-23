package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SetTitleTexts keeps a film's or a series' texts in language (a language
// offered besides the default): its name, overview and details, as TMDB
// gives them in it.
func (s *Store) SetTitleTexts(ctx context.Context, language string, t Title, details []byte, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO catalog_title_texts (kind, tmdb, language, name, overview, details, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (kind, tmdb, language) DO UPDATE SET name = excluded.name, overview = excluded.overview,
			details = excluded.details, fetched_at = excluded.fetched_at`,
		t.Kind, t.TMDB, language, t.Name, t.Overview, string(details), at.Unix())
	return err
}

// SetSeasonTexts keeps a season's texts in language, its episodes'
// included.
func (s *Store) SetSeasonTexts(ctx context.Context, language, series string, se Season) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_season_texts (series, season, language, name, overview)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (series, season, language) DO UPDATE SET name = excluded.name, overview = excluded.overview`,
		series, se.Number, language, se.Name, se.Overview); err != nil {
		return err
	}
	for _, e := range se.Episodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_episode_texts (series, season, episode, language, name, overview)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (series, season, episode, language) DO UPDATE SET name = excluded.name,
				overview = excluded.overview`, series, se.Number, e.Number, language, e.Name, e.Overview); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TextsToFetch lists library films and series without texts in language,
// or with texts fetched before before, with their seasons as the library
// has them.
func (s *Store) TextsToFetch(ctx context.Context, language string, before time.Time, limit int) ([]ArtworkTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, tmdb FROM (SELECT DISTINCT kind, tmdb FROM catalog_items
			WHERE kind IN ('movie', 'series') AND tmdb IS NOT NULL) t
		WHERE NOT EXISTS (SELECT 1 FROM catalog_title_texts x WHERE x.kind = t.kind AND x.tmdb = t.tmdb
			AND x.language = ? AND x.fetched_at >= ?)
		LIMIT ?`, language, before.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtworkTarget
	for rows.Next() {
		var t ArtworkTarget
		if err := rows.Scan(&t.Kind, &t.TMDB); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// HasTexts reports whether a film's or a series' texts in language were
// fetched at or after since.
func (s *Store) HasTexts(ctx context.Context, language, kind, tmdb string, since time.Time) (bool, error) {
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT fetched_at FROM catalog_title_texts WHERE kind = ? AND tmdb = ? AND language = ?`,
		kind, tmdb, language).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && at >= since.Unix(), err
}

// translate puts t's texts in language, where they're kept, and says
// whether they all were (t's own; for a season or an episode, its series'
// too).
func (s *Store) translate(ctx context.Context, t *TitleInfo, language string) (bool, error) {
	r := t.Ref
	tmdb := r.TMDB()
	if tmdb == "" {
		return true, nil // known by its file only: no texts
	}
	kind := r.Kind
	if kind != "movie" {
		kind = "series"
	}
	var name, overview, details string
	err := s.db.QueryRowContext(ctx, `SELECT name, overview, details FROM catalog_title_texts
		WHERE kind = ? AND tmdb = ? AND language = ?`, kind, tmdb, language).Scan(&name, &overview, &details)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch r.Kind {
	case "movie", "series":
		var d TitleDetails
		if err := json.Unmarshal([]byte(details), &d); err != nil {
			return false, err
		}
		t.Name, t.Overview = or(name, t.Name), or(overview, t.Overview)
		// The translated details' texts, the rest as kept.
		t.Details.Genres, t.Details.Tagline = d.Genres, d.Tagline
		for i := range t.Details.Cast {
			if i < len(d.Cast) && d.Cast[i].Name == t.Details.Cast[i].Name {
				t.Details.Cast[i].Character = or(d.Cast[i].Character, t.Details.Cast[i].Character)
			}
		}
		for i := range t.Details.Seasons {
			for _, se := range d.Seasons {
				if se.Number == t.Details.Seasons[i].Number {
					t.Details.Seasons[i].Name = or(se.Name, t.Details.Seasons[i].Name)
				}
			}
		}
		return true, nil
	case "season", "episode":
		t.SeriesName = or(name, t.SeriesName)
		var seasonName, seasonOverview string
		err := s.db.QueryRowContext(ctx, `SELECT name, overview FROM catalog_season_texts WHERE series = ? AND season = ? AND language = ?`,
			tmdb, r.Season, language).Scan(&seasonName, &seasonOverview)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if r.Kind == "season" {
			t.Name, t.Overview = or(seasonName, t.Name), or(seasonOverview, t.Overview)
			return true, nil
		}
		t.SeasonName = or(seasonName, t.SeasonName)
		var epName, epOverview string
		err = s.db.QueryRowContext(ctx, `SELECT name, overview FROM catalog_episode_texts
			WHERE series = ? AND season = ? AND episode = ? AND language = ?`, tmdb, r.Season, r.Episode, language).
			Scan(&epName, &epOverview)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		t.Name, t.Overview = or(epName, t.Name), or(epOverview, t.Overview)
		return true, nil
	}
	return true, nil
}
