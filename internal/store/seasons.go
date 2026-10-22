package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TitleDetails is a title's page as the catalog keeps it (as JSON, in
// catalog_titles.details): what TMDB says beyond its name, year and
// overview.
type TitleDetails struct {
	Genres  []string     `json:"genres"`
	Cast    []CastMember `json:"cast"`
	Seasons []SeasonInfo `json:"seasons"`
	// Runtime is in minutes: a film's, or a series' usual episode's.
	Runtime int    `json:"runtime,omitempty"`
	Tagline string `json:"tagline,omitempty"`
}

// CastMember is someone in a title's cast.
type CastMember struct {
	Name      string `json:"name"`
	Character string `json:"character"`
	Profile   string `json:"profile,omitempty"` // TMDB's path of their photo
}

// SeasonInfo is a season as its series' page lists it.
type SeasonInfo struct {
	Number       int    `json:"number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episodeCount"`
}

// Season is a season of a series, with its episodes, from TMDB.
type Season struct {
	Number    int
	Name      string
	Overview  string
	AirDate   string // YYYY-MM-DD, or ""
	Poster    string // a TMDB image path, or ""
	Episodes  []Episode
	FetchedAt time.Time
}

// Episode is one episode of a season, from TMDB.
type Episode struct {
	Number   int
	Name     string
	Overview string
	AirDate  string
	Runtime  int // minutes, 0 when unknown
	Still    string
}

// SetSeason replaces a series' season (by the series' TMDB ID), its
// episodes included, fetched at at.
func (s *Store) SetSeason(ctx context.Context, series string, se Season, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM catalog_seasons WHERE series = ? AND season = ?`, series, se.Number); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_seasons (series, season, name, overview, air_date, poster, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, series, se.Number, se.Name, se.Overview, nullString(se.AirDate),
		nullString(se.Poster), at.Unix()); err != nil {
		return err
	}
	for _, e := range se.Episodes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_episodes (series, season, episode, name, overview, air_date, runtime, still)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, series, se.Number, e.Number, e.Name, e.Overview, nullString(e.AirDate),
			nullInt(e.Runtime), nullString(e.Still)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SeasonByTMDB returns a series' season with its episodes, in order, or nil
// when the catalog doesn't have it.
func (s *Store) SeasonByTMDB(ctx context.Context, series string, number int) (*Season, error) {
	se := Season{Number: number}
	var fetched int64
	err := s.db.QueryRowContext(ctx, `SELECT name, overview, coalesce(air_date, ''), coalesce(poster, ''), fetched_at
		FROM catalog_seasons WHERE series = ? AND season = ?`, series, number).
		Scan(&se.Name, &se.Overview, &se.AirDate, &se.Poster, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	se.FetchedAt = time.Unix(fetched, 0)
	rows, err := s.db.QueryContext(ctx, `SELECT episode, name, overview, coalesce(air_date, ''), coalesce(runtime, 0),
		coalesce(still, '') FROM catalog_episodes WHERE series = ? AND season = ? ORDER BY episode`, series, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Episode
		if err := rows.Scan(&e.Number, &e.Name, &e.Overview, &e.AirDate, &e.Runtime, &e.Still); err != nil {
			return nil, err
		}
		se.Episodes = append(se.Episodes, e)
	}
	return &se, rows.Err()
}

// PurgeSeasons deletes the seasons (and their episodes) not fetched again
// since before.
func (s *Store) PurgeSeasons(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM catalog_seasons WHERE fetched_at < ?`, before.Unix())
	return err
}
