package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/overfin/server/internal/lang"
)

// Languages are the languages the operator offers (regional tags, such as
// "fr-CA") and the default among them. Until the operator sets them, they
// come from TMDB's language and the ratings country ("fr" in "CA" gives
// fr-CA), else English (United States). set is false until then.
func (s *Store) Languages(ctx context.Context) (offered []string, def string, set bool, err error) {
	var list, d []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'languages'`).Scan(&list)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'default-language'`).Scan(&d)
	}
	switch {
	case err == nil:
		return strings.Split(string(list), ","), string(d), true, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, "", false, err
	}
	tmdb, err := s.TMDBLanguage(ctx)
	if err != nil {
		return nil, "", false, err
	}
	country, err := s.RatingsCountry(ctx)
	if err != nil {
		return nil, "", false, err
	}
	def = lang.Region(tmdb, country)
	if def == "" {
		def = "en-US"
	}
	return []string{def}, def, false, nil
}

// SetLanguages saves the languages offered and the default, which must be
// one of them. TMDB is asked in the default (its regional tag); when that
// changes, its data is fetched again (SetTMDBLanguage), and changed
// reports it.
func (s *Store) SetLanguages(ctx context.Context, offered []string, def string) (changed bool, err error) {
	old, err := s.TMDBLanguage(ctx)
	if err != nil {
		return false, err
	}
	for name, value := range map[string]string{"languages": strings.Join(offered, ","), "default-language": def} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES (?, ?)
			ON CONFLICT (name) DO UPDATE SET value = excluded.value`, name, []byte(value)); err != nil {
			return false, err
		}
	}
	if def != old {
		return true, s.SetTMDBLanguage(ctx, def)
	}
	return false, nil
}

// SyncTMDBLanguage asks TMDB in the default language when it was asked in
// another (an install from before regional languages asked "fr" for
// fr-CA): its data is fetched again then, and changed says so.
func (s *Store) SyncTMDBLanguage(ctx context.Context) (changed bool, err error) {
	_, def, set, err := s.Languages(ctx)
	if err != nil || !set {
		return false, err
	}
	old, err := s.TMDBLanguage(ctx)
	if err != nil || old == def {
		return false, err
	}
	return true, s.SetTMDBLanguage(ctx, def)
}

// SetAccountLanguage changes an account's language.
func (s *Store) SetAccountLanguage(ctx context.Context, accountID, tag string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET language = ? WHERE id = ?`, tag, accountID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// LanguageOf is the language an account uses: its own when it's still
// offered, else the default.
func (s *Store) LanguageOf(ctx context.Context, a *Account) (string, error) {
	offered, def, _, err := s.Languages(ctx)
	if err != nil {
		return "", err
	}
	if slices.Contains(offered, a.Language) {
		return a.Language, nil
	}
	return def, nil
}

// tmdbTokenContext binds the sealed TMDB token to its row.
const tmdbTokenContext = "tmdb token"

// TMDBToken is the operator's TMDB token, as the panel or the setup saved it;
// ok is false without one.
func (s *Store) TMDBToken(ctx context.Context) (token string, ok bool, err error) {
	var sealed []byte
	err = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE name = 'tmdb-token'`).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	k, err := s.box.Open(sealed, tmdbTokenContext)
	if err != nil {
		return "", false, fmt.Errorf("store: TMDB's token can't be read with this secret key: %w", err)
	}
	return string(k), true, nil
}

// SetTMDBToken saves the operator's TMDB token, sealed; "" forgets it.
func (s *Store) SetTMDBToken(ctx context.Context, token string) error {
	if token == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE name = 'tmdb-token'`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta (name, value) VALUES ('tmdb-token', ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value`, s.box.Seal([]byte(token), tmdbTokenContext))
	return err
}
