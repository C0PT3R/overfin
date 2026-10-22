package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// People, from TMDB (migration 024).

// Person is a person as Overfin keeps them.
type Person struct {
	TMDB       string
	Name       string
	Biography  string
	Birthday   string // YYYY-MM-DD, or ""
	Deathday   string
	Birthplace string
	Department string
	Profile    string // a TMDB image path, or ""
	Credits    []PersonCredit
	FetchedAt  time.Time
	// Page: their whole page is kept, not only what a search found.
	Page bool
}

// PersonCredit is one of a person's films or series, and what they did in
// it.
type PersonCredit struct {
	Kind string `json:"kind"` // movie, series
	TMDB string `json:"tmdb"`
	Role string `json:"role,omitempty"`
	Crew bool   `json:"crew,omitempty"`
}

// SetPerson keeps a person, fetched at at, with their credits' titles.
func (s *Store) SetPerson(ctx context.Context, p Person, titles []Title, at time.Time) error {
	if err := s.PutTitles(ctx, titles, at); err != nil {
		return err
	}
	credits, err := json.Marshal(p.Credits)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tmdb_people (id, name, biography, birthday, deathday, birthplace, department,
			profile, credits, fetched_at, page) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT (id) DO UPDATE SET name = excluded.name, biography = excluded.biography, birthday = excluded.birthday,
			deathday = excluded.deathday, birthplace = excluded.birthplace, department = excluded.department,
			profile = excluded.profile, credits = excluded.credits, fetched_at = excluded.fetched_at, page = 1`,
		p.TMDB, p.Name, p.Biography, nullString(p.Birthday), nullString(p.Deathday), p.Birthplace, p.Department,
		nullString(p.Profile), string(credits), at.Unix())
	return err
}

// PersonByTMDB returns a person, or nil when Overfin doesn't keep them.
func (s *Store) PersonByTMDB(ctx context.Context, id string) (*Person, error) {
	p := Person{TMDB: id}
	var birthday, deathday, profile sql.NullString
	var credits string
	var fetched int64
	err := s.db.QueryRowContext(ctx, `SELECT name, biography, birthday, deathday, birthplace, department, profile, credits, fetched_at, page
		FROM tmdb_people WHERE id = ?`, id).Scan(&p.Name, &p.Biography, &birthday, &deathday, &p.Birthplace, &p.Department,
		&profile, &credits, &fetched, &p.Page)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Birthday, p.Deathday, p.Profile, p.FetchedAt = birthday.String, deathday.String, profile.String, time.Unix(fetched, 0)
	if err := json.Unmarshal([]byte(credits), &p.Credits); err != nil {
		return nil, err
	}
	return &p, nil
}

// IsPersonPhoto says whether path is a kept person's photo.
func (s *Store) IsPersonPhoto(ctx context.Context, path string) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM tmdb_people WHERE profile = ?)`, path).Scan(&ok)
	return ok, err
}

// PurgePeople deletes the people not fetched again since before.
func (s *Store) PurgePeople(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tmdb_people WHERE fetched_at < ?`, before.Unix())
	return err
}
