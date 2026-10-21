// Package watchlist is the watchlist module: each profile's own list of
// titles to watch, movies, whole shows, seasons or single episodes, as the
// person chooses. A show on it is the show itself, so its new episodes come
// along.
//
// It's built only on Overfin's public module contract, and is the example
// to follow when writing one.
package watchlist

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/C0PT3R/overfin-server/internal/feature"
)

//go:embed migrations/*.sql
var files embed.FS

// Max caps one profile's watchlist.
const Max = 1000

// Module is the watchlist module.
type Feature struct{}

func (Feature) Info() feature.Info {
	migrations, _ := fs.Sub(files, "migrations")
	return feature.Info{
		Name:        "watchlist",
		Needs:       []string{"library"},
		Description: "Each profile's own list of movies, shows, seasons and episodes to watch",
		Migrations:  migrations,
	}
}

func (Feature) Setup(h feature.Host) error {
	w := handlers{db: h.DB(), log: h.Logger()}
	for pattern, fn := range map[string]feature.Handler{
		"GET /api/v1/me/watchlist":         w.list,
		"GET /api/v1/me/watchlist/{id}":    w.has,
		"PUT /api/v1/me/watchlist/{id}":    w.add,
		"DELETE /api/v1/me/watchlist/{id}": w.remove,
	} {
		if err := h.Handle(pattern, fn); err != nil {
			return err
		}
	}
	return nil
}

// ForgetProfiles deletes deleted profiles' watchlists.
func (Feature) ForgetProfiles(ctx context.Context, db *sql.DB, profileIDs []string) error {
	for _, id := range profileIDs {
		if _, err := db.ExecContext(ctx, `DELETE FROM watchlist WHERE profile_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ForgetAccount has nothing to do: watchlists belong to profiles.
func (Feature) ForgetAccount(context.Context, *sql.DB, string) error { return nil }

// Export includes the profile's watchlist in "Download my data".
func (Feature) Export(ctx context.Context, h feature.Host, c *feature.Caller) (any, error) {
	return items(ctx, h.DB(), c)
}

type handlers struct {
	db  *sql.DB
	log *slog.Logger
}

// watchable are the kinds of titles one watches.
var watchable = map[string]bool{"movie": true, "series": true, "season": true, "episode": true, "video": true, "musicVideo": true}

// ids lists a profile's watchlist, most recently added first.
func ids(ctx context.Context, db *sql.DB, profileID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT item_id FROM watchlist WHERE profile_id = ? ORDER BY added_at DESC, rowid DESC`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// items reads the watchlist's titles through the backend as the caller, in
// the list's order: what the profile can't see, or what's gone, is left out.
func items(ctx context.Context, db *sql.DB, c *feature.Caller) ([]feature.Item, error) {
	list, err := ids(ctx, db, c.ProfileID)
	if err != nil {
		return nil, err
	}
	found, err := c.Backend.Items(ctx, list)
	if err != nil {
		return nil, err
	}
	byID := map[string]feature.Item{}
	for _, it := range found {
		byID[it.ID] = it
	}
	out := []feature.Item{}
	for _, id := range list {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (w handlers) list(c *feature.Caller, rw http.ResponseWriter, r *http.Request) {
	out, err := items(r.Context(), w.db, c)
	if err != nil {
		w.log.Error("reading a watchlist failed", "err", err)
		feature.Problem(rw, feature.Internal, "")
		return
	}
	feature.JSON(rw, map[string]any{"items": out})
}

func (w handlers) has(c *feature.Caller, rw http.ResponseWriter, r *http.Request) {
	id, ok := feature.ItemID(r.PathValue("id"))
	if !ok {
		feature.Problem(rw, feature.NotFound, "")
		return
	}
	var one int
	err := w.db.QueryRowContext(r.Context(), `SELECT 1 FROM watchlist WHERE profile_id = ? AND item_id = ?`, c.ProfileID, id).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		feature.Problem(rw, feature.NotFound, "")
	case err != nil:
		feature.Problem(rw, feature.Internal, "")
	default:
		feature.NoContent(rw)
	}
}

func (w handlers) add(c *feature.Caller, rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := feature.ItemID(r.PathValue("id"))
	if !ok {
		feature.Problem(rw, feature.NotFound, "")
		return
	}
	// Only a title this profile can see, and only kinds one watches.
	it, err := c.Backend.Item(ctx, id)
	if errors.Is(err, feature.ErrNotFound) {
		feature.Problem(rw, feature.NotFound, "")
		return
	} else if err != nil {
		w.log.Error("looking up a title failed", "err", err)
		feature.Problem(rw, feature.Internal, "")
		return
	}
	if !watchable[it.Kind] {
		feature.Problem(rw, feature.InvalidRequest, "only movies, shows, seasons, episodes and videos go on a watchlist")
		return
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		feature.Problem(rw, feature.Internal, "")
		return
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM watchlist WHERE profile_id = ?`, c.ProfileID).Scan(&n); err != nil {
		feature.Problem(rw, feature.Internal, "")
		return
	}
	if n >= Max {
		feature.Problem(rw, feature.InvalidRequest, "the watchlist is full")
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, c.ProfileID, id, time.Now().Unix()); err != nil || tx.Commit() != nil {
		feature.Problem(rw, feature.Internal, "")
		return
	}
	feature.NoContent(rw)
}

func (w handlers) remove(c *feature.Caller, rw http.ResponseWriter, r *http.Request) {
	id, ok := feature.ItemID(r.PathValue("id"))
	if !ok {
		feature.Problem(rw, feature.NotFound, "")
		return
	}
	if _, err := w.db.ExecContext(r.Context(), `DELETE FROM watchlist WHERE profile_id = ? AND item_id = ?`, c.ProfileID, id); err != nil {
		feature.Problem(rw, feature.Internal, "")
		return
	}
	feature.NoContent(rw)
}
