package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/overfin/server/internal/ids"
)

// ---- Users (identity mode "managed") ---------------------------------------------

// User is someone who signs in with Overfin itself.
type User struct {
	Subject string
	Email   string
	// PasswordHash is empty when they set no password.
	PasswordHash string
	Admin        bool
	Disabled     bool
	Created      time.Time
}

// ErrEmailTaken: another person has that email.
var ErrEmailTaken = errors.New("store: email already used")

const userColumns = `subject, email, coalesce(password_hash, ''), admin, disabled_at IS NOT NULL, created_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var p User
	var created int64
	err := sc.Scan(&p.Subject, &p.Email, &p.PasswordHash, &p.Admin, &p.Disabled, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Created = time.Unix(created, 0).UTC()
	return &p, nil
}

// CreateUser adds someone by email.
func (s *Store) CreateUser(ctx context.Context, email string, admin bool) (*User, error) {
	p := &User{Subject: ids.New(ids.User), Email: email, Admin: admin, Created: s.now().UTC()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (subject, email, admin, created_at) VALUES (?, ?, ?, ?)`,
		p.Subject, p.Email, boolInt(admin), p.Created.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("store: creating user: %w", err)
	}
	return p, nil
}

// ImportUser adds someone with the subject they already have, as an
// identity provider's user ID: their account stays theirs when the server
// moves to managed sign-in.
func (s *Store) ImportUser(ctx context.Context, subject, email string, admin bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO users (subject, email, admin, created_at) VALUES (?, ?, ?, ?)`,
		subject, email, boolInt(admin), s.now().Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrEmailTaken
	}
	return err
}

// SubjectsInGroup lists the subjects whose live logins carry a group: who
// the identity provider said was in it, at their last login.
func (s *Store) SubjectsInGroup(ctx context.Context, group string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT subject, groups FROM logins WHERE expires_at > ?`, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var subject, groups string
		if err := rows.Scan(&subject, &groups); err != nil {
			return nil, err
		}
		if slices.Contains(strings.Split(groups, groupSep), group) {
			out[subject] = true
		}
	}
	return out, rows.Err()
}

// UserByEmail finds someone by email, whatever its case.
func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

// UserBySubject finds someone by their subject.
func (s *Store) UserBySubject(ctx context.Context, subject string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE subject = ?`, subject))
}

// Users lists everyone, by email.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		p, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// HasAdmin reports whether anyone is an admin.
func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE admin = 1`).Scan(&n)
	return n > 0, err
}

// SetPassword sets someone's password hash; empty removes their password.
func (s *Store) SetPassword(ctx context.Context, subject, hash string) error {
	var v any
	if hash != "" {
		v = hash
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE subject = ?`, v, subject)
	return oneRow(res, err)
}

// SetEmail changes someone's email.
func (s *Store) SetEmail(ctx context.Context, subject, email string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET email = ? WHERE subject = ?`, email, subject)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrEmailTaken
	}
	return oneRow(res, err)
}

// SetUserAdmin makes someone an admin, or not.
func (s *Store) SetUserAdmin(ctx context.Context, subject string, admin bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET admin = ? WHERE subject = ?`, boolInt(admin), subject)
	return oneRow(res, err)
}

// SetUserDisabled disables someone, ending their logins at once, or
// enables them again.
func (s *Store) SetUserDisabled(ctx context.Context, subject string, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var at any
	if disabled {
		at = s.now().Unix()
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET disabled_at = ? WHERE subject = ?`, at, subject)
	if err := oneRow(res, err); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM logins WHERE subject = ?`, subject); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteUser removes someone and ends their logins.
func (s *Store) DeleteUser(ctx context.Context, subject string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE subject = ?`, subject)
	if err := oneRow(res, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM logins WHERE subject = ?`, subject); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Sign-in codes --------------------------------------------------------------------

// SigninCode is a code sent by email. Only keyed hashes of the code, of
// the link and of the asking browser's cookie are kept.
type SigninCode struct {
	Email    string
	CodeHash []byte
	LinkHash []byte
	Binding  []byte
	// Purpose is "signin", or "fresh" (confirming a login before a
	// sensitive change).
	Purpose string
	Expires time.Time
}

// MaxCodeAttempts is how many wrong codes end a code.
const MaxCodeAttempts = 5

// AddSigninCode records a code sent.
func (s *Store) AddSigninCode(ctx context.Context, c SigninCode) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO signin_codes (id, email, code_hash, link_hash, binding, purpose, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ids.New(ids.Code), c.Email, c.CodeHash, c.LinkHash, c.Binding, c.Purpose, c.Expires.Unix(), s.now().Unix())
	return err
}

// CodesSentSince counts the codes sent to an email since a time, live or
// not, for rate limits.
func (s *Store) CodesSentSince(ctx context.Context, email string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM signin_codes WHERE email = ? AND created_at >= ?`,
		email, since.Unix()).Scan(&n)
	return n, err
}

// TakeSigninCode checks a typed code against the live codes sent to that
// email, for that purpose, from that browser. A match uses up every code of
// the email; a miss counts against each, and a code with too many misses
// goes.
func (s *Store) TakeSigninCode(ctx context.Context, email, purpose string, binding, codeHash []byte) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, code_hash FROM signin_codes
		WHERE email = ? AND purpose = ? AND binding = ? AND expires_at > ? AND attempts < ?`,
		email, purpose, binding, s.now().Unix(), MaxCodeAttempts)
	if err != nil {
		return false, err
	}
	var live []string
	match := false
	for rows.Next() {
		var id string
		var h []byte
		if err := rows.Scan(&id, &h); err != nil {
			rows.Close()
			return false, err
		}
		live = append(live, id)
		if subtle.ConstantTimeCompare(h, codeHash) == 1 {
			match = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if match {
		_, err = tx.ExecContext(ctx, `DELETE FROM signin_codes WHERE email = ?`, email)
	} else {
		for _, id := range live {
			if _, err = tx.ExecContext(ctx, `UPDATE signin_codes SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
				break
			}
		}
	}
	if err != nil {
		return false, err
	}
	return match, tx.Commit()
}

// TakeSigninLink uses up the code whose link was opened, and every other
// code of its email, returning its email and purpose.
func (s *Store) TakeSigninLink(ctx context.Context, linkHash []byte) (email, purpose string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `
		SELECT email, purpose FROM signin_codes WHERE link_hash = ? AND expires_at > ? AND attempts < ?`,
		linkHash, s.now().Unix(), MaxCodeAttempts).Scan(&email, &purpose)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM signin_codes WHERE email = ?`, email); err != nil {
		return "", "", err
	}
	return email, purpose, tx.Commit()
}

// DeleteOldSigninCodes removes codes a day past their expiry: kept that
// long, they still count for rate limits.
func (s *Store) DeleteOldSigninCodes(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM signin_codes WHERE expires_at <= ?`, s.now().Add(-24*time.Hour).Unix())
	return err
}
