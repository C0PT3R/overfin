// Package store keeps Overfin's own data in SQLite: accounts, profiles and
// sessions. It holds only what Overfin needs to run (SECURITY.md rule 40).
//
// Upstream tokens are sealed before they are written, and sessions are found
// by the SHA-256 of their cookie value, so the database alone yields neither
// usable tokens nor usable sessions.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/C0PT3R/overfin/internal/ids"
	"github.com/C0PT3R/overfin/internal/seal"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when a lookup finds nothing.
var ErrNotFound = errors.New("store: not found")

// Store is the database. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	box *seal.Box
	now func() time.Time
}

// Open opens (or creates) the database at path and applies any pending
// migrations. box seals upstream tokens.
func Open(ctx context.Context, path string, box *seal.Box) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	// One connection: SQLite allows one writer at a time anyway, and this
	// keeps every transaction simple and correct at Overfin's scale.
	db.SetMaxOpenConns(1)

	s := &Store{db: db, box: box, now: time.Now}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies migrations newer than the database's user_version, each in
// its own transaction.
func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("store: reading schema version: %w", err)
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for i, name := range names {
		version := i + 1 // 001_… is version 1, and so on
		if version <= current {
			continue
		}
		if !strings.HasPrefix(name, fmt.Sprintf("migrations/%03d_", version)) {
			return fmt.Errorf("store: migration %s is out of sequence", name)
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: applying %s: %w", name, err)
		}
		// PRAGMA doesn't take parameters; version is an int we computed.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- Accounts and profiles --------------------------------------------------

// Account is one person from the identity provider.
type Account struct {
	ID       string
	Subject  string
	Created  time.Time
	Disabled bool
}

// Profile is one profile of an account. JellyfinToken is the plain token,
// opened from its sealed form; it never leaves the server.
type Profile struct {
	ID               string
	AccountID        string
	Name             string
	Kind             string // "standard" or "kids"
	Owner            bool
	JellyfinUserID   string
	JellyfinUsername string
	JellyfinToken    string
	Language         string
	RatingCeiling    string
	HasPIN           bool
}

// NewOwner describes the owner profile created with an account.
type NewOwner struct {
	Name             string
	JellyfinUserID   string
	JellyfinUsername string
	JellyfinToken    string
}

// CreateAccount creates an account and its owner profile together: an
// account never exists without its owner.
func (s *Store) CreateAccount(ctx context.Context, subject string, owner NewOwner) (*Account, *Profile, error) {
	now := s.now().UTC()
	acct := &Account{ID: ids.New(ids.Account), Subject: subject, Created: now}
	prof := &Profile{
		ID: ids.New(ids.Profile), AccountID: acct.ID, Name: owner.Name, Kind: "standard", Owner: true,
		JellyfinUserID: owner.JellyfinUserID, JellyfinUsername: owner.JellyfinUsername, JellyfinToken: owner.JellyfinToken,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback() // does nothing once committed

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounts (id, subject, created_at) VALUES (?, ?, ?)`,
		acct.ID, subject, now.Unix()); err != nil {
		return nil, nil, fmt.Errorf("store: creating account: %w", err)
	}
	if err := insertProfile(ctx, tx, s.box, prof, now); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return acct, prof, nil
}

func insertProfile(ctx context.Context, tx *sql.Tx, box *seal.Box, p *Profile, now time.Time) error {
	sealed := box.Seal([]byte(p.JellyfinToken), p.ID)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO profiles (id, account_id, name, kind, owner, jellyfin_user_id,
			jellyfin_username, jellyfin_token, language, rating_ceiling, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.AccountID, p.Name, p.Kind, boolInt(p.Owner), p.JellyfinUserID,
		p.JellyfinUsername, sealed, nullable(p.Language), nullable(p.RatingCeiling), now.Unix())
	if err != nil {
		return fmt.Errorf("store: creating profile: %w", err)
	}
	return nil
}

// AccountBySubject finds the account of an identity provider user.
func (s *Store) AccountBySubject(ctx context.Context, subject string) (*Account, error) {
	var a Account
	var created int64
	var disabled sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, subject, created_at, disabled_at FROM accounts WHERE subject = ?`, subject,
	).Scan(&a.ID, &a.Subject, &created, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Created = time.Unix(created, 0).UTC()
	a.Disabled = disabled.Valid
	return &a, nil
}

// Profile returns one profile, only if it belongs to accountID (SECURITY.md
// rule 23): another account's profile is simply not found.
func (s *Store) Profile(ctx context.Context, accountID, profileID string) (*Profile, error) {
	row := s.db.QueryRowContext(ctx, profileSelect+` WHERE id = ? AND account_id = ?`, profileID, accountID)
	return s.scanProfile(row)
}

// Profiles lists an account's profiles, owner first.
func (s *Store) Profiles(ctx context.Context, accountID string) ([]*Profile, error) {
	rows, err := s.db.QueryContext(ctx, profileSelect+` WHERE account_id = ? ORDER BY owner DESC, name`, accountID)
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

const profileSelect = `
	SELECT id, account_id, name, kind, owner, jellyfin_user_id, jellyfin_username,
		jellyfin_token, language, rating_ceiling, pin_hash IS NOT NULL
	FROM profiles`

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface{ Scan(dest ...any) error }

func (s *Store) scanProfile(sc scanner) (*Profile, error) {
	var p Profile
	var owner int
	var sealed []byte
	var language, ceiling sql.NullString
	err := sc.Scan(&p.ID, &p.AccountID, &p.Name, &p.Kind, &owner, &p.JellyfinUserID,
		&p.JellyfinUsername, &sealed, &language, &ceiling, &p.HasPIN)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	token, err := s.box.Open(sealed, p.ID)
	if err != nil {
		// Tampered with, moved from another row, or a different key.
		return nil, fmt.Errorf("store: profile %s: %w", p.ID, err)
	}
	p.Owner = owner == 1
	p.JellyfinToken = string(token)
	p.Language = language.String
	p.RatingCeiling = ceiling.String
	return &p, nil
}

// ---- Sessions ---------------------------------------------------------------

// Session is one logged-in browser or device.
type Session struct {
	ID              string
	AccountID       string
	ActiveProfileID string
	Device          string
	Created         time.Time
	LastActive      time.Time
	FreshLogin      time.Time
	Expires         time.Time
}

// CreateSession starts a session and returns it with its secret token, the
// cookie value. Only the token's hash is stored; the token itself can't be
// recovered later.
func (s *Store) CreateSession(ctx context.Context, accountID, profileID, device string, lifetime time.Duration) (*Session, string, error) {
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := s.now().UTC()
	sess := &Session{
		ID: ids.New(ids.Session), AccountID: accountID, ActiveProfileID: profileID, Device: device,
		Created: now, LastActive: now, FreshLogin: now, Expires: now.Add(lifetime),
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, token_hash, account_id, active_profile_id, device,
			created_at, last_active_at, fresh_login_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hashToken(token), accountID, nullable(profileID), nullable(device),
		now.Unix(), now.Unix(), now.Unix(), sess.Expires.Unix())
	if err != nil {
		return nil, "", fmt.Errorf("store: creating session: %w", err)
	}
	return sess, token, nil
}

// SessionByToken finds a live session by its cookie value. Expired sessions
// are not found.
func (s *Store) SessionByToken(ctx context.Context, token string) (*Session, error) {
	var sess Session
	var profile, device sql.NullString
	var created, active, expires int64
	var fresh sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, active_profile_id, device, created_at, last_active_at,
			fresh_login_at, expires_at
		FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), s.now().Unix(),
	).Scan(&sess.ID, &sess.AccountID, &profile, &device, &created, &active, &fresh, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.ActiveProfileID = profile.String
	sess.Device = device.String
	sess.Created = time.Unix(created, 0).UTC()
	sess.LastActive = time.Unix(active, 0).UTC()
	sess.Expires = time.Unix(expires, 0).UTC()
	if fresh.Valid {
		sess.FreshLogin = time.Unix(fresh.Int64, 0).UTC()
	}
	return &sess, nil
}

// DeleteSession ends a session, only if it belongs to accountID.
func (s *Store) DeleteSession(ctx context.Context, accountID, sessionID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND account_id = ?`, sessionID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteExpiredSessions removes sessions past their expiry and returns how
// many there were.
func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// hashToken is SHA-256. Session tokens are 256 random bits, so a plain hash
// is enough: there is nothing to guess.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullable stores empty strings as NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
