package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/overfin/server/internal/ids"
)

// Requests, made in Seerr on behalf of accounts (see migration 020).

// SeerrUser returns an account's Seerr user ID; ok is false when it has
// none yet.
func (s *Store) SeerrUser(ctx context.Context, accountID string) (id int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT seerr_id FROM seerr_users WHERE account_id = ?`, accountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// SetSeerrUser links an account to its Seerr user.
func (s *Store) SetSeerrUser(ctx context.Context, accountID string, seerrID int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO seerr_users (account_id, seerr_id, created_at) VALUES (?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET seerr_id = excluded.seerr_id, created_at = excluded.created_at`,
		accountID, seerrID, at.Unix())
	return err
}

// Request is one of a profile's requests.
type Request struct {
	ID        string
	AccountID string
	ProfileID string
	// Title is the film's or the series' key; Seasons, the seasons asked
	// for (none: a film, or every season).
	Title   string
	Seasons []int
	SeerrID int
	// Status is pending, approved, declined, processing, available,
	// partiallyAvailable or failed.
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AddRequest keeps a new request, and gives it its ID.
func (s *Store) AddRequest(ctx context.Context, r *Request) error {
	r.ID = ids.New(ids.Request)
	var seasons any
	if len(r.Seasons) > 0 {
		b, _ := json.Marshal(r.Seasons)
		seasons = string(b)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO requests (id, account_id, profile_id, title, seasons, seerr_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.AccountID, r.ProfileID, r.Title, seasons, r.SeerrID, r.Status,
		r.CreatedAt.Unix(), r.CreatedAt.Unix())
	return err
}

const requestColumns = `id, account_id, profile_id, title, seasons, seerr_id, status, created_at, updated_at`

func scanRequests(rows *sql.Rows) ([]Request, error) {
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var r Request
		var seasons sql.NullString
		var created, updated int64
		if err := rows.Scan(&r.ID, &r.AccountID, &r.ProfileID, &r.Title, &seasons, &r.SeerrID, &r.Status, &created, &updated); err != nil {
			return nil, err
		}
		if seasons.Valid {
			json.Unmarshal([]byte(seasons.String), &r.Seasons)
		}
		r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ProfileRequests lists the requests of an account's profiles, the latest
// first.
func (s *Store) ProfileRequests(ctx context.Context, accountID string, profileIDs []string) ([]Request, error) {
	if len(profileIDs) == 0 {
		return nil, nil
	}
	args := []any{accountID}
	for _, id := range profileIDs {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+requestColumns+` FROM requests
		WHERE account_id = ? AND profile_id IN (`+marks(len(profileIDs))+`) ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	return scanRequests(rows)
}

// AccountRequests lists an account's requests for titles (by key), any of
// its profiles'.
func (s *Store) AccountRequests(ctx context.Context, accountID string, titles []string) ([]Request, error) {
	if len(titles) == 0 {
		return nil, nil
	}
	args := []any{accountID}
	for _, t := range titles {
		args = append(args, t)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+requestColumns+` FROM requests
		WHERE account_id = ? AND title IN (`+marks(len(titles))+`) ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	return scanRequests(rows)
}

// RequestByID returns one of an account's requests, or ErrNotFound.
func (s *Store) RequestByID(ctx context.Context, accountID, id string) (*Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+requestColumns+` FROM requests WHERE account_id = ? AND id = ?`, accountID, id)
	if err != nil {
		return nil, err
	}
	all, err := scanRequests(rows)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, ErrNotFound
	}
	return &all[0], nil
}

// SetRequestStatus records a request's status as Seerr has it now.
func (s *Store) SetRequestStatus(ctx context.Context, id, status string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE requests SET status = ?, updated_at = ? WHERE id = ? AND status != ?`,
		status, at.Unix(), id, status)
	return err
}

// DeleteRequest forgets a request.
func (s *Store) DeleteRequest(ctx context.Context, accountID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE account_id = ? AND id = ?`, accountID, id)
	return err
}
