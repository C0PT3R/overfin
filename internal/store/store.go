// Package store keeps Overfin's own data in SQLite: accounts, profiles and
// sessions. It holds only what Overfin needs to run (SECURITY.md rule 40).
//
// Upstream tokens are sealed before they are written, and sessions are found
// by the SHA-256 of their cookie value, so the database alone yields neither
// usable tokens nor usable sessions.
package store

import (
	"context"
	"crypto/hmac"
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

	"github.com/overfin/server/internal/ids"
	"github.com/overfin/server/internal/seal"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when a lookup finds nothing.
var ErrNotFound = errors.New("store: not found")

// Store is the database. It is safe for concurrent use.
type Store struct {
	db     *sql.DB
	box    *seal.Box
	now    func() time.Time
	forget Forgetter
}

// Open opens (or creates) the database at path and applies any pending
// migrations. box seals upstream tokens.
func Open(ctx context.Context, path string, box *seal.Box) (*Store, error) {
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		// Temporary data (sorts, unions) stays in memory: the container's
		// file system is read-only, with no temporary directory.
		"&_pragma=temp_store(2)"
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
		if err := s.apply(ctx, name, string(body), version); err != nil {
			return err
		}
	}
	return nil
}

// foreignKeysOff marks a migration that rebuilds a table: it runs with
// foreign keys off (dropping the old table would otherwise delete or
// unlink every row pointing at it), and they're checked before it commits.
const foreignKeysOff = "-- overfin: foreign keys off\n"

// apply runs one migration in its own transaction.
func (s *Store) apply(ctx context.Context, name, body string, version int) (err error) {
	// The pragma works only outside a transaction; the store has one
	// connection, so it holds for the transaction below.
	if strings.HasPrefix(body, foreignKeysOff) {
		if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return err
		}
		defer func() {
			if _, ferr := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); ferr != nil && err == nil {
				err = ferr
			}
		}()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("store: applying %s: %w", name, err)
	}
	if strings.HasPrefix(body, foreignKeysOff) {
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return err
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return fmt.Errorf("store: applying %s: it would break references between tables", name)
		}
	}
	// PRAGMA doesn't take parameters; version is an int we computed.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Accounts and profiles --------------------------------------------------

// Account is one person from the identity provider.
type Account struct {
	ID       string
	Subject  string
	Created  time.Time
	Disabled bool
	// Set by the operator: a label, and when access ends (zero: never).
	Label   string
	Expires time.Time
	// Template is the account template it follows: what it may do.
	Template Template
}

// Template is an account template: what accounts on it may do.
type Template struct {
	ID          string
	Name        string
	MaxStreams  int
	MaxProfiles int
	// Accounts is how many accounts follow it (filled by Templates only).
	Accounts int
}

// DefaultTemplate is the template that exists from the start: one profile,
// one stream, until the operator edits it. Every account without another
// template is on it. It can't be deleted.
const DefaultTemplate = "tmpl_default"

// Expired reports whether the account's access has ended.
func (a *Account) Expired(now time.Time) bool {
	return !a.Expires.IsZero() && !now.Before(a.Expires)
}

const accountSelect = `SELECT a.id, a.subject, a.created_at, a.disabled_at, a.label, a.expires_at,
		t.id, t.name, t.max_streams, t.max_profiles
	FROM accounts a JOIN account_templates t ON t.id = COALESCE(a.template_id, 'tmpl_default')`

func scanAccount(sc scanner) (*Account, error) {
	var a Account
	var created int64
	var disabled, expires sql.NullInt64
	var label sql.NullString
	err := sc.Scan(&a.ID, &a.Subject, &created, &disabled, &label, &expires,
		&a.Template.ID, &a.Template.Name, &a.Template.MaxStreams, &a.Template.MaxProfiles)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.Created = time.Unix(created, 0).UTC()
	a.Disabled = disabled.Valid
	a.Label = label.String
	if expires.Valid {
		a.Expires = time.Unix(expires.Int64, 0).UTC()
	}
	return &a, nil
}

// Profile is one profile of an account. Only a profile that brought a
// Jellyfin user of its own (an adopted account) has JellyfinUserID and
// JellyfinToken (the plain token, opened from its sealed form; it never
// leaves the server): Overfin's one Jellyfin account serves everyone.
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
	// MovieCeiling and SeriesCeiling are a kids profile's highest allowed
	// certifications, on the ratings country's scales; empty until chosen.
	MovieCeiling  string
	SeriesCeiling string
	// Libraries, when not nil, are the only libraries the profile gets
	// (by Jellyfin ID); BlockedTags are hidden from it.
	Libraries   []string
	BlockedTags []string
	HasPIN      bool
	Created     time.Time
	// SubtitleLanguage is the language it last chose for subtitles; ""
	// for none.
	SubtitleLanguage string
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
	// A profile has a Jellyfin user of its own only when it brought one
	// (an adopted account); Overfin's one account serves the others.
	var sealed any
	if p.JellyfinUserID != "" {
		sealed = box.Seal([]byte(p.JellyfinToken), p.ID)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO profiles (id, account_id, name, kind, owner, jellyfin_user_id,
			jellyfin_username, jellyfin_token, language, movie_ceiling, series_ceiling, libraries, blocked_tags, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.AccountID, p.Name, p.Kind, boolInt(p.Owner), nullable(p.JellyfinUserID),
		nullable(p.JellyfinUsername), sealed, nullable(p.Language), nullable(p.MovieCeiling), nullable(p.SeriesCeiling),
		jsonList(p.Libraries), jsonList(p.BlockedTags), now.Unix())
	if err != nil {
		return fmt.Errorf("store: creating profile: %w", err)
	}
	return nil
}

// JellyfinUserLinked reports whether a Jellyfin user already belongs to a
// profile.
func (s *Store) JellyfinUserLinked(ctx context.Context, jellyfinUserID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM profiles WHERE jellyfin_user_id = ?`, jellyfinUserID).Scan(&n)
	return n > 0, err
}

// AccountBySubject finds the account of an identity provider user.
func (s *Store) AccountBySubject(ctx context.Context, subject string) (*Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, accountSelect+` WHERE a.subject = ?`, subject))
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
		jellyfin_token, language, movie_ceiling, series_ceiling, libraries, blocked_tags, pin_hash IS NOT NULL, created_at,
		subtitle_language
	FROM profiles`

// scanner is what *sql.Row and *sql.Rows have in common.
type scanner interface{ Scan(dest ...any) error }

func (s *Store) scanProfile(sc scanner) (*Profile, error) {
	var p Profile
	var owner int
	var sealed []byte
	var language, movie, series, jfUser, jfName, subtitles sql.NullString
	var libraries, tags *string
	var created int64
	err := sc.Scan(&p.ID, &p.AccountID, &p.Name, &p.Kind, &owner, &jfUser,
		&jfName, &sealed, &language, &movie, &series, &libraries, &tags, &p.HasPIN, &created, &subtitles)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.JellyfinUserID, p.JellyfinUsername = jfUser.String, jfName.String
	if sealed != nil {
		token, err := s.box.Open(sealed, p.ID)
		if err != nil {
			// Tampered with, moved from another row, or a different key.
			return nil, fmt.Errorf("store: profile %s: %w", p.ID, err)
		}
		p.JellyfinToken = string(token)
	}
	p.Owner = owner == 1
	p.Language, p.SubtitleLanguage = language.String, subtitles.String
	p.MovieCeiling, p.SeriesCeiling = movie.String, series.String
	p.Libraries, p.BlockedTags = scanList(libraries), scanList(tags)
	p.Created = time.Unix(created, 0).UTC()
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
		Created: now, LastActive: now, Expires: now.Add(lifetime),
	}
	// No fresh login yet: a new session only proves the identity provider
	// still knows the user, not that they just logged in. Only step-up
	// sets it.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (id, token_hash, account_id, active_profile_id, device,
			created_at, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, hashToken(token), accountID, nullable(profileID), nullable(device),
		now.Unix(), now.Unix(), sess.Expires.Unix())
	if err != nil {
		return nil, "", fmt.Errorf("store: creating session: %w", err)
	}
	return sess, token, nil
}

// SessionByToken finds a live session by its cookie value. Expired sessions
// are not found.
func (s *Store) SessionByToken(ctx context.Context, token string) (*Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, account_id, active_profile_id, device, created_at, last_active_at,
			fresh_login_at, expires_at
		FROM sessions WHERE token_hash = ? AND expires_at > ?`,
		hashToken(token), s.now().Unix())
	sess, err := scanSession(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sess, err
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

// ---- More profiles ------------------------------------------------------------

// ErrNameTaken is returned when an account already has a profile with that
// name (names are unique per account, a namespace of its own).
var ErrNameTaken = errors.New("store: profile name already used in this account")

// ErrProfileLimit is returned when an account has as many profiles as allowed.
var ErrProfileLimit = errors.New("store: profile limit reached")

// NewProfile describes an additional profile and its Jellyfin user.
type NewProfile struct {
	Name             string
	Kind             string // "standard" or "kids"
	Language         string
	MovieCeiling     string
	SeriesCeiling    string
	Libraries        []string // nil: every library
	BlockedTags      []string
	JellyfinUserID   string
	JellyfinUsername string
	JellyfinToken    string
}

// AddProfile creates a profile in an account, refusing a name already used
// there or more than maxProfiles profiles in all (0 means no limit).
func (s *Store) AddProfile(ctx context.Context, accountID string, np NewProfile, maxProfiles int) (*Profile, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var count, sameName int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(name = ?), 0) FROM profiles WHERE account_id = ?`,
		np.Name, accountID).Scan(&count, &sameName); err != nil {
		return nil, err
	}
	if sameName > 0 {
		return nil, ErrNameTaken
	}
	if maxProfiles > 0 && count >= maxProfiles {
		return nil, ErrProfileLimit
	}
	p := &Profile{
		ID: ids.New(ids.Profile), AccountID: accountID, Name: np.Name, Kind: np.Kind,
		JellyfinUserID: np.JellyfinUserID, JellyfinUsername: np.JellyfinUsername, JellyfinToken: np.JellyfinToken,
		Language: np.Language, MovieCeiling: np.MovieCeiling, SeriesCeiling: np.SeriesCeiling,
		Libraries: np.Libraries, BlockedTags: np.BlockedTags,
	}
	if err := insertProfile(ctx, tx, s.box, p, s.now().UTC()); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// RenameProfile changes a profile's name, within its account.
func (s *Store) RenameProfile(ctx context.Context, accountID, profileID, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE account_id = ? AND name = ? AND id != ?`,
		accountID, name, profileID).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return ErrNameTaken
	}
	res, err := tx.ExecContext(ctx, `UPDATE profiles SET name = ? WHERE id = ? AND account_id = ?`, name, profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// SetProfileLanguage changes a profile's preferred metadata language.
func (s *Store) SetProfileLanguage(ctx context.Context, accountID, profileID, language string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET language = ? WHERE id = ? AND account_id = ?`,
		nullable(language), profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubtitleLanguage keeps the language a profile chose for subtitles
// ("" for none).
func (s *Store) SetSubtitleLanguage(ctx context.Context, accountID, profileID, language string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET subtitle_language = ? WHERE id = ? AND account_id = ?`,
		nullable(language), profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- More sessions --------------------------------------------------------------

// Sessions lists an account's live sessions, most recently active first.
func (s *Store) Sessions(ctx context.Context, accountID string) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_id, active_profile_id, device, created_at, last_active_at, fresh_login_at, expires_at
		FROM sessions WHERE account_id = ? AND expires_at > ? ORDER BY last_active_at DESC`,
		accountID, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// SetActiveProfile switches a session to a profile of the same account.
func (s *Store) SetActiveProfile(ctx context.Context, sessionID, accountID, profileID string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET active_profile_id = ?
		WHERE id = ? AND account_id = ?
		  AND EXISTS (SELECT 1 FROM profiles WHERE id = ? AND account_id = ?)`,
		profileID, sessionID, accountID, profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchSession records activity and slides the expiry forward.
func (s *Store) TouchSession(ctx context.Context, sessionID string, lifetime time.Duration) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_active_at = ?, expires_at = ? WHERE id = ?`,
		now.Unix(), now.Add(lifetime).Unix(), sessionID)
	return err
}

// DeleteOtherSessions ends all of an account's sessions except one.
func (s *Store) DeleteOtherSessions(ctx context.Context, accountID, keepID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE account_id = ? AND id != ?`, accountID, keepID)
	return err
}

func scanSession(sc scanner) (*Session, error) {
	var sess Session
	var profile, device sql.NullString
	var created, active, expires int64
	var fresh sql.NullInt64
	if err := sc.Scan(&sess.ID, &sess.AccountID, &profile, &device, &created, &active, &fresh, &expires); err != nil {
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

// ---- Kids profiles and PINs -------------------------------------------------------

// ErrPINExists is returned when setting a PIN on a profile that has one;
// changing an existing PIN needs a fresh login (rule 21).
var ErrPINExists = errors.New("store: profile already has a PIN")

// SetKind changes a profile's kind and ceilings, within its account.
func (s *Store) SetKind(ctx context.Context, accountID, profileID, kind, movieCeiling, seriesCeiling string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET kind = ?, movie_ceiling = ?, series_ceiling = ?
		WHERE id = ? AND account_id = ?`,
		kind, nullable(movieCeiling), nullable(seriesCeiling), profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPIN stores a PIN's keyed hash on a profile that has none yet.
func (s *Store) SetPIN(ctx context.Context, accountID, profileID string, hash []byte) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET pin_hash = ?, pin_failures = 0, pin_failed_at = NULL, pin_locked_until = NULL
		WHERE id = ? AND account_id = ? AND pin_hash IS NULL`, hash, profileID, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Profile(ctx, accountID, profileID); err != nil {
			return ErrNotFound
		}
		return ErrPINExists
	}
	return nil
}

// PINState is what checking a PIN needs.
type PINState struct {
	Hash        []byte
	Failures    int
	FailedAt    time.Time
	LockedUntil time.Time
}

// PIN returns a profile's PIN state, within its account.
func (s *Store) PIN(ctx context.Context, accountID, profileID string) (*PINState, error) {
	var st PINState
	var failedAt, lockedUntil sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT pin_hash, pin_failures, pin_failed_at, pin_locked_until FROM profiles
		WHERE id = ? AND account_id = ?`, profileID, accountID,
	).Scan(&st.Hash, &st.Failures, &failedAt, &lockedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if failedAt.Valid {
		st.FailedAt = time.Unix(failedAt.Int64, 0).UTC()
	}
	if lockedUntil.Valid {
		st.LockedUntil = time.Unix(lockedUntil.Int64, 0).UTC()
	}
	return &st, nil
}

// RecordPIN stores the outcome of a PIN attempt.
func (s *Store) RecordPIN(ctx context.Context, profileID string, failures int, failedAt, lockedUntil time.Time) error {
	toUnix := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t.Unix()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE profiles SET pin_failures = ?, pin_failed_at = ?, pin_locked_until = ? WHERE id = ?`,
		failures, toUnix(failedAt), toUnix(lockedUntil), profileID)
	return err
}

// ---- Step-up -----------------------------------------------------------------------

// SetFreshLogin records that the person behind a session just confirmed a
// login with the identity provider.
func (s *Store) SetFreshLogin(ctx context.Context, accountID, sessionID string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET fresh_login_at = ? WHERE id = ? AND account_id = ?`,
		at.Unix(), sessionID, accountID)
	return oneRow(res, err)
}

// UnlockPINs lifts every PIN lock and forgets wrong attempts in an account,
// after its owner logged in again (rule 22).
func (s *Store) UnlockPINs(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET pin_failures = 0, pin_failed_at = NULL, pin_locked_until = NULL
		WHERE account_id = ?`, accountID)
	return err
}

// ReplacePIN sets a profile's PIN whether or not it has one; nil removes it.
// Callers make sure a fresh login came first.
func (s *Store) ReplacePIN(ctx context.Context, accountID, profileID string, hash []byte) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE profiles SET pin_hash = ?, pin_failures = 0, pin_failed_at = NULL, pin_locked_until = NULL
		WHERE id = ? AND account_id = ?`, hash, profileID, accountID)
	return oneRow(res, err)
}

// DeleteProfile deletes a profile that isn't the owner's. Sessions using it
// fall back to the owner profile.
func (s *Store) DeleteProfile(ctx context.Context, accountID, profileID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM profiles WHERE id = ? AND account_id = ? AND owner = 0`,
		profileID, accountID)
	if err := oneRow(res, err); err != nil {
		return err
	}
	s.forgotten(ctx, []string{profileID}, "")
	return nil
}

// oneRow turns "no row changed" into ErrNotFound.
func oneRow(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Logins (identity mode "oidc") --------------------------------------------------

// Login is a visitor logged in through Overfin's own OpenID Connect login.
type Login struct {
	ID           string
	Subject      string
	Groups       []string
	RefreshToken string
	Checked      time.Time
	LastActive   time.Time
	Expires      time.Time
}

// groupSep separates stored group names; it can't appear in one.
const groupSep = "\n"

// CreateLogin records a login and returns its secret token, the cookie value.
func (s *Store) CreateLogin(ctx context.Context, subject string, groups []string, refreshToken string, lifetime time.Duration) (*Login, string, error) {
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	l := &Login{ID: ids.New(ids.Login), Subject: subject, Groups: groups, RefreshToken: refreshToken,
		Checked: now, LastActive: now, Expires: now.Add(lifetime)}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO logins (id, token_hash, subject, groups, refresh_token, checked_at, created_at, last_active_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.ID, hashToken(token), subject, strings.Join(groups, groupSep),
		s.box.Seal([]byte(refreshToken), "login refresh token "+l.ID), now.Unix(), now.Unix(), now.Unix(), l.Expires.Unix())
	if err != nil {
		return nil, "", fmt.Errorf("store: creating login: %w", err)
	}
	return l, token, nil
}

// LoginByToken finds a live login by its cookie value.
func (s *Store) LoginByToken(ctx context.Context, token string) (*Login, error) {
	return s.scanLogin(s.db.QueryRowContext(ctx, `
		SELECT id, subject, groups, refresh_token, checked_at, last_active_at, expires_at
		FROM logins WHERE token_hash = ? AND expires_at > ?`, hashToken(token), s.now().Unix()))
}

// LoginByID finds a live login by its ID.
func (s *Store) LoginByID(ctx context.Context, id string) (*Login, error) {
	return s.scanLogin(s.db.QueryRowContext(ctx, `
		SELECT id, subject, groups, refresh_token, checked_at, last_active_at, expires_at
		FROM logins WHERE id = ? AND expires_at > ?`, id, s.now().Unix()))
}

func (s *Store) scanLogin(row *sql.Row) (*Login, error) {
	var l Login
	var groups string
	var sealed []byte
	var checked, active, expires int64
	err := row.Scan(&l.ID, &l.Subject, &groups, &sealed, &checked, &active, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	refresh, err := s.box.Open(sealed, "login refresh token "+l.ID)
	if err != nil {
		return nil, fmt.Errorf("store: login %s: %w", l.ID, err)
	}
	l.RefreshToken = string(refresh)
	if groups != "" {
		l.Groups = strings.Split(groups, groupSep)
	}
	l.Checked = time.Unix(checked, 0).UTC()
	l.LastActive = time.Unix(active, 0).UTC()
	l.Expires = time.Unix(expires, 0).UTC()
	return &l, nil
}

// LoginChecked records a successful re-check: new groups and refresh token,
// and the login's lifetime slides.
func (s *Store) LoginChecked(ctx context.Context, id string, groups []string, refreshToken string, lifetime time.Duration) error {
	now := s.now()
	res, err := s.db.ExecContext(ctx, `
		UPDATE logins SET groups = ?, refresh_token = ?, checked_at = ?, last_active_at = ?, expires_at = ?
		WHERE id = ?`, strings.Join(groups, groupSep), s.box.Seal([]byte(refreshToken), "login refresh token "+id),
		now.Unix(), now.Unix(), now.Add(lifetime).Unix(), id)
	return oneRow(res, err)
}

// DeleteLogin ends a login.
func (s *Store) DeleteLogin(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM logins WHERE id = ?`, id)
	return err
}

// DeleteExpiredLogins removes logins past their expiry.
func (s *Store) DeleteExpiredLogins(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM logins WHERE expires_at <= ?`, s.now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- Key check and recovery ---------------------------------------------------------

// ErrWrongKey means the secret key isn't the one the database was made with:
// nothing sealed with the old one can be read.
var ErrWrongKey = errors.New("store: OVERFIN_SECRET_KEY is not the key this database was created with")

// CheckKey compares the key's check value with the one stored. A database
// without one (older, or new) records it.
func (s *Store) CheckKey(ctx context.Context, check []byte) error {
	var stored []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'key-check'`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return s.SetKeyCheck(ctx, check)
	}
	if err != nil {
		return err
	}
	if !hmac.Equal(stored, check) {
		return ErrWrongKey
	}
	return nil
}

// KeyMatches tells whether check is the key check recorded in the database
// at path, opened read-only so nothing changes; true when none is recorded
// yet.
func KeyMatches(ctx context.Context, path string, check []byte) (bool, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return false, fmt.Errorf("store: %w", err)
	}
	defer db.Close()
	var stored []byte
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'key-check'`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: reading the key check: %w", err)
	}
	return hmac.Equal(stored, check), nil
}

// SetKeyCheck records the key's check value, replacing any other.
func (s *Store) SetKeyCheck(ctx context.Context, check []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('key-check', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, check)
	return err
}

// Unreadable is a profile whose Jellyfin token can't be opened with the
// current key.
type Unreadable struct {
	ProfileID        string
	JellyfinUserID   string
	JellyfinUsername string
}

// UnreadableTokens lists the profiles whose Jellyfin token can't be opened.
func (s *Store) UnreadableTokens(ctx context.Context) ([]Unreadable, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, jellyfin_user_id, jellyfin_username, jellyfin_token FROM profiles
		WHERE jellyfin_token IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unreadable
	for rows.Next() {
		var u Unreadable
		var sealed []byte
		var name sql.NullString
		if err := rows.Scan(&u.ProfileID, &u.JellyfinUserID, &name, &sealed); err != nil {
			return nil, err
		}
		if _, err := s.box.Open(sealed, u.ProfileID); err != nil {
			u.JellyfinUsername = name.String
			out = append(out, u)
		}
	}
	return out, rows.Err()
}

// SetJellyfinToken replaces a profile's Jellyfin token.
func (s *Store) SetJellyfinToken(ctx context.Context, profileID, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET jellyfin_token = ? WHERE id = ?`,
		s.box.Seal([]byte(token), profileID), profileID)
	return oneRow(res, err)
}

// ForgetSealed removes what can't be used after a key change: PINs (keyed
// with the old key) and logins (their refresh tokens were sealed with it).
func (s *Store) ForgetSealed(ctx context.Context) (pins, logins int64, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET pin_hash = NULL, pin_failures = 0,
		pin_failed_at = NULL, pin_locked_until = NULL WHERE pin_hash IS NOT NULL`)
	if err != nil {
		return 0, 0, err
	}
	pins, _ = res.RowsAffected()
	if res, err = s.db.ExecContext(ctx, `DELETE FROM logins`); err != nil {
		return pins, 0, err
	}
	logins, _ = res.RowsAffected()
	return pins, logins, nil
}

// Linked is a profile whose Jellyfin user was adopted in "link" mode: it
// still has its own name and password.
type Linked struct {
	ProfileID      string
	JellyfinUserID string
}

// LinkedProfiles lists the profiles whose Jellyfin user keeps its own name.
// Users Overfin created or moved all have opaque "of-" names.
func (s *Store) LinkedProfiles(ctx context.Context) ([]Linked, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, jellyfin_user_id FROM profiles WHERE jellyfin_username NOT LIKE 'of-%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Linked
	for rows.Next() {
		var l Linked
		if err := rows.Scan(&l.ProfileID, &l.JellyfinUserID); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetJellyfinUser records a profile's Jellyfin user's new name and token,
// after a move.
func (s *Store) SetJellyfinUser(ctx context.Context, profileID, username, token string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET jellyfin_username = ?, jellyfin_token = ? WHERE id = ?`,
		username, s.box.Seal([]byte(token), profileID), profileID)
	return oneRow(res, err)
}

// ---- Account deletion -----------------------------------------------------------------

// DeleteAccount deletes an account and everything the database holds about
// it: profiles, sessions, PINs (by cascade), and the logins of its identity.
func (s *Store) DeleteAccount(ctx context.Context, accountID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var subject string
	if err := tx.QueryRowContext(ctx, `SELECT subject FROM accounts WHERE id = ?`, accountID).Scan(&subject); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var profileIDs []string
	rows, err := tx.QueryContext(ctx, `SELECT id FROM profiles WHERE account_id = ?`, accountID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		profileIDs = append(profileIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// With managed sign-in, the person's email and password go too.
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE subject = ?`, subject); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM logins WHERE subject = ?`, subject); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, accountID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Modules' own databases forget them too.
	s.forgotten(ctx, profileIDs, accountID)
	return nil
}

// ---- Feature settings ----------------------------------------------------------

// Features returns the stored feature settings (JSON), and false when none
// are stored yet.
func (s *Store) Features(ctx context.Context) ([]byte, bool, error) {
	var v []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'features'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return v, err == nil, err
}

// SetFeatures stores the feature settings (JSON).
func (s *Store) SetFeatures(ctx context.Context, v []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('features', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, v)
	return err
}

// ---- Jellyfin -----------------------------------------------------------------

// The Jellyfin server, as the setup page saved it: its address as is, its
// administrator's key sealed, bound to its row.
const jellyfinKeyContext = "jellyfin admin key"

// Jellyfin returns the Jellyfin server's address and key, and false when the
// setup page hasn't saved them yet.
func (s *Store) Jellyfin(ctx context.Context) (url, key string, ok bool, err error) {
	return jellyfinSettings(ctx, s.db, s.box)
}

// JellyfinServer is the ID of the Jellyfin server saved by the setup page;
// empty when none is saved.
func (s *Store) JellyfinServer(ctx context.Context) (string, error) {
	var id []byte
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'jellyfin-id'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return string(id), err
}

// SetJellyfin saves the Jellyfin server's address, key and server ID.
func (s *Store) SetJellyfin(ctx context.Context, url, key, serverID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range map[string][]byte{
		"jellyfin-url": []byte(url),
		"jellyfin-key": s.box.Seal([]byte(key), jellyfinKeyContext),
		"jellyfin-id":  []byte(serverID),
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadJellyfin is Jellyfin for the database at path, opened read-only so
// nothing changes (for overfin -check).
func ReadJellyfin(ctx context.Context, path string, box *seal.Box) (url, key string, ok bool, err error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", "", false, fmt.Errorf("store: %w", err)
	}
	defer db.Close()
	return jellyfinSettings(ctx, db, box)
}

func jellyfinSettings(ctx context.Context, db *sql.DB, box *seal.Box) (url, key string, ok bool, err error) {
	var u, sealed []byte
	err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'jellyfin-url'`).Scan(&u)
	if err == nil {
		err = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'jellyfin-key'`).Scan(&sealed)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("store: reading Jellyfin's settings: %w", err)
	}
	k, err := box.Open(sealed, jellyfinKeyContext)
	if err != nil {
		return "", "", false, fmt.Errorf("store: Jellyfin's key can't be read with this secret key: %w", err)
	}
	return string(u), string(k), true, nil
}

// ---- Control panel: accounts --------------------------------------------------------

// AccountByID finds an account by its ID.
func (s *Store) AccountByID(ctx context.Context, id string) (*Account, error) {
	return scanAccount(s.db.QueryRowContext(ctx, accountSelect+` WHERE a.id = ?`, id))
}

// Accounts lists every account, newest first.
func (s *Store) Accounts(ctx context.Context) ([]*Account, error) {
	rows, err := s.db.QueryContext(ctx, accountSelect+` ORDER BY a.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccountLabel sets an account's label; empty removes it.
func (s *Store) SetAccountLabel(ctx context.Context, id, label string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET label = ? WHERE id = ?`, nullable(label), id)
	return oneRow(res, err)
}

// SetAccountDisabled disables an account, or enables it again.
func (s *Store) SetAccountDisabled(ctx context.Context, id string, disabled bool) error {
	var at any
	if disabled {
		at = s.now().Unix()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET disabled_at = ? WHERE id = ?`, at, id)
	return oneRow(res, err)
}

// SetAccountExpiry sets when an account's access ends; zero: never.
func (s *Store) SetAccountExpiry(ctx context.Context, id string, at time.Time) error {
	var v any
	if !at.IsZero() {
		v = at.Unix()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET expires_at = ? WHERE id = ?`, v, id)
	return oneRow(res, err)
}

// PINLocks lists the profiles of an account whose PIN is locked now.
func (s *Store) PINLocks(ctx context.Context, accountID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM profiles WHERE account_id = ? AND pin_locked_until > ?`,
		accountID, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// UnlockPIN lifts one profile's PIN lock and forgets its wrong attempts.
func (s *Store) UnlockPIN(ctx context.Context, profileID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET pin_failures = 0, pin_failed_at = NULL, pin_locked_until = NULL
		WHERE id = ?`, profileID)
	return oneRow(res, err)
}

// ---- Modules -------------------------------------------------------------------------

// ---- Account templates ----------------------------------------------------------

// ErrTemplateInUse: a template accounts still follow can't be deleted.
var ErrTemplateInUse = errors.New("store: template in use")

// ErrTemplateNameTaken: another template has that name.
var ErrTemplateNameTaken = errors.New("store: template name already used")

func (s *Store) templateNameTaken(ctx context.Context, name, exceptID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM account_templates WHERE name = ? AND id != ?`, name, exceptID).Scan(&n)
	return n > 0, err
}

// Templates lists the account templates, Default first, with how many
// accounts follow each.
func (s *Store) Templates(ctx context.Context) ([]*Template, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.name, t.max_streams, t.max_profiles,
			(SELECT count(*) FROM accounts a WHERE COALESCE(a.template_id, 'tmpl_default') = t.id)
		FROM account_templates t ORDER BY t.id != 'tmpl_default', t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.MaxStreams, &t.MaxProfiles, &t.Accounts); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// CreateTemplate adds an account template.
func (s *Store) CreateTemplate(ctx context.Context, name string, maxStreams, maxProfiles int) (*Template, error) {
	if taken, err := s.templateNameTaken(ctx, name, ""); err != nil {
		return nil, err
	} else if taken {
		return nil, ErrTemplateNameTaken
	}
	t := &Template{ID: ids.New(ids.Template), Name: name, MaxStreams: maxStreams, MaxProfiles: maxProfiles}
	_, err := s.db.ExecContext(ctx, `INSERT INTO account_templates (id, name, max_streams, max_profiles, created_at)
		VALUES (?, ?, ?, ?, ?)`, t.ID, name, maxStreams, maxProfiles, s.now().Unix())
	return t, err
}

// UpdateTemplate changes a template; every account on it follows at once.
func (s *Store) UpdateTemplate(ctx context.Context, id, name string, maxStreams, maxProfiles int) error {
	if taken, err := s.templateNameTaken(ctx, name, id); err != nil {
		return err
	} else if taken {
		return ErrTemplateNameTaken
	}
	res, err := s.db.ExecContext(ctx, `UPDATE account_templates SET name = ?, max_streams = ?, max_profiles = ? WHERE id = ?`,
		name, maxStreams, maxProfiles, id)
	return oneRow(res, err)
}

// DeleteTemplate deletes a template no account follows. Default can't be
// deleted.
func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	if id == DefaultTemplate {
		return ErrTemplateInUse
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE template_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrTemplateInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM account_templates WHERE id = ?`, id)
	return oneRow(res, err)
}

// SetAccountTemplate puts an account on a template.
func (s *Store) SetAccountTemplate(ctx context.Context, accountID, templateID string) error {
	var v any = templateID
	if templateID == DefaultTemplate {
		v = nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET template_id = ?
		WHERE id = ? AND EXISTS (SELECT 1 FROM account_templates WHERE id = ?)`, v, accountID, templateID)
	return oneRow(res, err)
}
