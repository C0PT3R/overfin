package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// mailPasswordContext binds the SMTP password's seal to its row.
const mailPasswordContext = "smtp password"

// Mail returns the email settings the control panel saved (as JSON), the
// SMTP password, and false when none are saved.
func (s *Store) Mail(ctx context.Context) (settings []byte, password string, ok bool, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'mail'`).Scan(&settings)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'mail-password'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	pw, err := s.box.Open(sealed, mailPasswordContext)
	if err != nil {
		return nil, "", false, fmt.Errorf("store: the SMTP password can't be read with this secret key: %w", err)
	}
	return settings, string(pw), true, nil
}

// SetMail saves the email settings and the SMTP password (sealed).
func (s *Store) SetMail(ctx context.Context, settings []byte, password string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"mail":          settings,
		"mail-password": s.box.Seal([]byte(password), mailPasswordContext),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ForgetMail removes the email settings: Overfin sends no more emails.
func (s *Store) ForgetMail(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name IN ('mail', 'mail-password')`)
	return err
}
