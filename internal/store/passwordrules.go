package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/overfin/server/internal/password"
)

// PasswordRules are what a new password must have; password.DefaultRules
// until the panel changes them.
func (s *Store) PasswordRules(ctx context.Context) (password.Rules, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'password-rules'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return password.DefaultRules, nil
	}
	if err != nil {
		return password.DefaultRules, err
	}
	r := password.DefaultRules
	if err := json.Unmarshal(raw, &r); err != nil || !r.Valid() {
		return password.DefaultRules, err
	}
	return r, nil
}

// SetPasswordRules changes them.
func (s *Store) SetPasswordRules(ctx context.Context, r password.Rules) error {
	if !r.Valid() {
		return errors.New("store: password rules: a length from 10 to 64")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('password-rules', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, raw)
	return err
}
