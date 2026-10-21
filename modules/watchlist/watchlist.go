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

	"github.com/C0PT3R/overfin-gateway/module"
)

//go:embed migrations/*.sql
var files embed.FS

// Max caps one profile's watchlist.
const Max = 1000

// Module is the watchlist module.
type Module struct{}

func (Module) Info() module.Info {
	migrations, _ := fs.Sub(files, "migrations")
	return module.Info{
		Name:        "watchlist",
		Needs:       []string{"library"},
		Description: "Each profile's own list of movies, shows, seasons and episodes to watch",
		Migrations:  migrations,
	}
}

func (Module) Setup(h module.Host) error {
	w := handlers{db: h.DB(), log: h.Logger()}
	for pattern, fn := range map[string]module.Handler{
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
func (Module) ForgetProfiles(ctx context.Context, db *sql.DB, profileIDs []string) error {
	for _, id := range profileIDs {
		if _, err := db.ExecContext(ctx, `DELETE FROM watchlist WHERE profile_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ForgetAccount has nothing to do: watchlists belong to profiles.
func (Module) ForgetAccount(context.Context, *sql.DB, string) error { return nil }

// Export includes the profile's watchlist in "Download my data".
func (Module) Export(ctx context.Context, h module.Host, c *module.Caller) (any, error) {
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
func items(ctx context.Context, db *sql.DB, c *module.Caller) ([]module.Item, error) {
	list, err := ids(ctx, db, c.ProfileID)
	if err != nil {
		return nil, err
	}
	found, err := c.Backend.Items(ctx, list)
	if err != nil {
		return nil, err
	}
	byID := map[string]module.Item{}
	for _, it := range found {
		byID[it.ID] = it
	}
	out := []module.Item{}
	for _, id := range list {
		if it, ok := byID[id]; ok {
			out = append(out, it)
		}
	}
	return out, nil
}

func (w handlers) list(c *module.Caller, rw http.ResponseWriter, r *http.Request) {
	out, err := items(r.Context(), w.db, c)
	if err != nil {
		w.log.Error("reading a watchlist failed", "err", err)
		module.Problem(rw, module.Internal, "")
		return
	}
	module.JSON(rw, map[string]any{"items": out})
}

func (w handlers) has(c *module.Caller, rw http.ResponseWriter, r *http.Request) {
	id, ok := module.ItemID(r.PathValue("id"))
	if !ok {
		module.Problem(rw, module.NotFound, "")
		return
	}
	var one int
	err := w.db.QueryRowContext(r.Context(), `SELECT 1 FROM watchlist WHERE profile_id = ? AND item_id = ?`, c.ProfileID, id).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		module.Problem(rw, module.NotFound, "")
	case err != nil:
		module.Problem(rw, module.Internal, "")
	default:
		module.NoContent(rw)
	}
}

func (w handlers) add(c *module.Caller, rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := module.ItemID(r.PathValue("id"))
	if !ok {
		module.Problem(rw, module.NotFound, "")
		return
	}
	// Only a title this profile can see, and only kinds one watches.
	it, err := c.Backend.Item(ctx, id)
	if errors.Is(err, module.ErrNotFound) {
		module.Problem(rw, module.NotFound, "")
		return
	} else if err != nil {
		w.log.Error("looking up a title failed", "err", err)
		module.Problem(rw, module.Internal, "")
		return
	}
	if !watchable[it.Kind] {
		module.Problem(rw, module.InvalidRequest, "only movies, shows, seasons, episodes and videos go on a watchlist")
		return
	}
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		module.Problem(rw, module.Internal, "")
		return
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM watchlist WHERE profile_id = ?`, c.ProfileID).Scan(&n); err != nil {
		module.Problem(rw, module.Internal, "")
		return
	}
	if n >= Max {
		module.Problem(rw, module.InvalidRequest, "the watchlist is full")
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO watchlist (profile_id, item_id, added_at) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, c.ProfileID, id, time.Now().Unix()); err != nil || tx.Commit() != nil {
		module.Problem(rw, module.Internal, "")
		return
	}
	module.NoContent(rw)
}

func (w handlers) remove(c *module.Caller, rw http.ResponseWriter, r *http.Request) {
	id, ok := module.ItemID(r.PathValue("id"))
	if !ok {
		module.Problem(rw, module.NotFound, "")
		return
	}
	if _, err := w.db.ExecContext(r.Context(), `DELETE FROM watchlist WHERE profile_id = ? AND item_id = ?`, c.ProfileID, id); err != nil {
		module.Problem(rw, module.Internal, "")
		return
	}
	module.NoContent(rw)
}
