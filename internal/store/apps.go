package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/overfin/server/internal/ids"
)

// AppKey is a key the operator gave an app.
type AppKey struct {
	ID, Name, Scope string
	Created         time.Time
	LastUsed        time.Time // zero: never
}

func hashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// CreateAppKey keeps a new key, by its hash only.
func (s *Store) CreateAppKey(ctx context.Context, name, scope, key string, at time.Time) (*AppKey, error) {
	k := &AppKey{ID: ids.New("app"), Name: name, Scope: scope, Created: at}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_keys (id, name, key_hash, scope, created_at) VALUES (?, ?, ?, ?, ?)`,
		k.ID, name, hashKey(key), scope, at.Unix())
	return k, err
}

// AppKeys lists the keys, the newest first.
func (s *Store) AppKeys(ctx context.Context) ([]AppKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, scope, created_at, coalesce(last_used_at, 0) FROM app_keys
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppKey
	for rows.Next() {
		var k AppKey
		var created, used int64
		if err := rows.Scan(&k.ID, &k.Name, &k.Scope, &created, &used); err != nil {
			return nil, err
		}
		k.Created = time.Unix(created, 0).UTC()
		if used > 0 {
			k.LastUsed = time.Unix(used, 0).UTC()
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAppKey revokes a key.
func (s *Store) DeleteAppKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM app_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AppKeyFor finds the key presented by an app, and notes its use.
func (s *Store) AppKeyFor(ctx context.Context, key string, at time.Time) (*AppKey, error) {
	var k AppKey
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, scope, created_at FROM app_keys WHERE key_hash = ?`, hashKey(key)).
		Scan(&k.ID, &k.Name, &k.Scope, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.Created = time.Unix(created, 0).UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE app_keys SET last_used_at = ? WHERE id = ?`, at.Unix(), k.ID)
	return &k, err
}

// WatchStats is what the profiles did with a film or a series, in total:
// never anything per person (SECURITY.md rule 40). A series counts its
// episodes.
type WatchStats struct {
	Kind             string // movie, series
	TMDB, IMDb, TVDB string
	// Plays are the times it was played to the end; Viewers, the accounts
	// that watched some of it; Favorites, the profiles that keep it as a
	// favorite.
	Plays, Viewers, Favorites int
	LastPlayed                time.Time // zero: never
	Changed                   time.Time
}

// AllWatchStats totals, for each film and series anyone watched or keeps
// as a favorite, what the profiles did with it; only those changed since
// since when it isn't zero.
func (s *Store) AllWatchStats(ctx context.Context, since time.Time) ([]WatchStats, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.title, p.account_id, t.play_count, t.played, t.favorite,
			coalesce(t.last_played_at, 0), t.updated_at
		FROM profile_titles t JOIN profiles p ON p.id = t.profile_id
		WHERE (t.play_count > 0 OR t.played = 1 OR t.favorite = 1) AND t.title NOT LIKE 'item/%'`)
	if err != nil {
		return nil, err
	}
	type total struct {
		WatchStats
		viewers map[string]bool
	}
	byKey := map[string]*total{}
	var order []string
	for rows.Next() {
		var title, account string
		var plays, played, favorite int
		var last, updated int64
		if err := rows.Scan(&title, &account, &plays, &played, &favorite, &last, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		r, ok := ParseTitleKey(title)
		if !ok {
			continue
		}
		top := r
		if r.Kind == "season" || r.Kind == "episode" {
			top = r.Series()
		}
		key := top.Key()
		t := byKey[key]
		if t == nil {
			t = &total{WatchStats: WatchStats{Kind: top.Kind}, viewers: map[string]bool{}}
			if top.Source == "tmdb" {
				t.TMDB = top.ID
			}
			byKey[key] = t
			order = append(order, key)
		}
		t.Plays += plays
		if plays > 0 || played == 1 {
			t.viewers[account] = true
		}
		if favorite == 1 && r.Kind == top.Kind {
			t.Favorites++
		}
		if last > 0 && time.Unix(last, 0).After(t.LastPlayed) {
			t.LastPlayed = time.Unix(last, 0).UTC()
		}
		if u := time.Unix(updated, 0).UTC(); u.After(t.Changed) {
			t.Changed = u
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The other IDs the libraries know each title by.
	out := make([]WatchStats, 0, len(order))
	for _, key := range order {
		t := byKey[key]
		if !since.IsZero() && t.Changed.Before(since) {
			continue
		}
		var imdb, tvdb, tmdb sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT max(imdb), max(tvdb), max(tmdb) FROM catalog_items
			WHERE title_key = ? AND kind IN ('movie', 'series')`, key).Scan(&imdb, &tvdb, &tmdb)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if t.TMDB == "" {
			t.TMDB = tmdb.String
		}
		t.IMDb, t.TVDB = imdb.String, tvdb.String
		if t.IMDb == "" && strings.HasPrefix(key, t.Kind+"/imdb/") {
			t.IMDb = strings.TrimPrefix(key, t.Kind+"/imdb/")
		}
		t.Viewers = len(t.viewers)
		out = append(out, t.WatchStats)
	}
	return out, nil
}
