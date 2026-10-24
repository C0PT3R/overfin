package store

import (
	"context"
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
	// Confirmed is false until someone who registered themselves opens the
	// link that confirms their email; MustChange, that they choose a new
	// password at their next sign-in.
	Confirmed  bool
	MustChange bool
}

// ErrEmailTaken: another person has that email.
var ErrEmailTaken = errors.New("store: email already used")

const userColumns = `subject, email, coalesce(password_hash, ''), admin, disabled_at IS NOT NULL, created_at,
	confirmed, must_change`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var p User
	var created int64
	err := sc.Scan(&p.Subject, &p.Email, &p.PasswordHash, &p.Admin, &p.Disabled, &created, &p.Confirmed, &p.MustChange)
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := oneRow(tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE subject = ?`, v, subject)); err != nil {
		return err
	}
	// A link to choose a new password, asked before, no longer works.
	if _, err := tx.ExecContext(ctx, `DELETE FROM email_links WHERE subject = ? AND purpose = 'reset'`, subject); err != nil {
		return err
	}
	return tx.Commit()
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

// ---- Registration and links by email --------------------------------------------------

// inviteContext binds a sealed invitation code to its person.
func inviteContext(subject string) string { return "invite " + subject }

// RegisterUser adds someone who registered themselves, with an
// invitation: their password's hash, the invitation's code (sealed), and
// not confirmed until they open the link sent to their email.
func (s *Store) RegisterUser(ctx context.Context, email, passwordHash, inviteCode string) (*User, error) {
	p := &User{Subject: ids.New(ids.User), Email: email, PasswordHash: passwordHash, Created: s.now().UTC()}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (subject, email, password_hash, created_at, confirmed, invite_code) VALUES (?, ?, ?, ?, 0, ?)`,
		p.Subject, p.Email, passwordHash, p.Created.Unix(), s.box.Seal([]byte(inviteCode), inviteContext(p.Subject)))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return nil, ErrEmailTaken
	}
	if err != nil {
		return nil, fmt.Errorf("store: registering user: %w", err)
	}
	return p, nil
}

// ConfirmUser marks a person's email confirmed, and gives the invitation
// they registered with ("" without one), forgetting it.
func (s *Store) ConfirmUser(ctx context.Context, subject string) (inviteCode string, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT invite_code FROM users WHERE subject = ?`, subject).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if sealed != nil {
		code, err := s.box.Open(sealed, inviteContext(subject))
		if err != nil {
			return "", fmt.Errorf("store: an invitation: %w", err)
		}
		inviteCode = string(code)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET confirmed = 1, invite_code = NULL WHERE subject = ?`, subject)
	return inviteCode, err
}

// SetMustChange says whether a person must choose a new password at their
// next sign-in.
func (s *Store) SetMustChange(ctx context.Context, subject string, must bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET must_change = ? WHERE subject = ?`, boolInt(must), subject)
	return err
}

// DeleteUnconfirmed removes the people who registered before a time and
// never confirmed their email.
func (s *Store) DeleteUnconfirmed(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE confirmed = 0 AND created_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// EmailLink is a link sent by email: to confirm the address ("verify"),
// or to choose a new password ("reset"). Only a keyed hash is kept.
type EmailLink struct {
	Subject  string
	LinkHash []byte
	Purpose  string
	Expires  time.Time
}

// AddEmailLink records a link sent; earlier links of the same purpose to
// that person stop working.
func (s *Store) AddEmailLink(ctx context.Context, l EmailLink) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM email_links WHERE subject = ? AND purpose = ?`, l.Subject, l.Purpose); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO email_links (id, subject, link_hash, purpose, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		ids.New(ids.Code), l.Subject, l.LinkHash, l.Purpose, l.Expires.Unix(), s.now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// TakeEmailLink uses up a link of a purpose, returning whose it is.
func (s *Store) TakeEmailLink(ctx context.Context, purpose string, linkHash []byte) (subject string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT subject FROM email_links WHERE link_hash = ? AND purpose = ? AND expires_at > ?`,
		linkHash, purpose, s.now().Unix()).Scan(&subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM email_links WHERE subject = ? AND purpose = ?`, subject, purpose); err != nil {
		return "", err
	}
	return subject, tx.Commit()
}

// DeleteOldEmailLinks removes the links past their expiry.
func (s *Store) DeleteOldEmailLinks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM email_links WHERE expires_at <= ?`, s.now().Unix())
	return err
}
