// Package hello is a tiny example module: one route that greets the calling
// profile and counts its visits in a table of its own. See MODULES.md.
package hello

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"net/http"

	"github.com/C0PT3R/overfin-gateway/module"
)

//go:embed migrations/*.sql ui
var files embed.FS

// Module is the hello module.
type Module struct{}

func (Module) Info() module.Info {
	migrations, _ := fs.Sub(files, "migrations")
	ui, _ := fs.Sub(files, "ui")
	return module.Info{
		Name:        "hello",
		Description: "An example module that greets the calling profile",
		Migrations:  migrations,
		AdminUI:     ui,
	}
}

func (Module) Setup(h module.Host) error {
	db := h.DB()
	// For the control panel tab: totals only, never who.
	if err := h.HandleAdmin("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		var profiles, visits int
		if err := db.QueryRowContext(r.Context(), `SELECT count(*), coalesce(sum(visits), 0) FROM hello_visits`).Scan(&profiles, &visits); err != nil {
			module.Problem(w, module.Internal, "")
			return
		}
		module.JSON(w, map[string]int{"profiles": profiles, "visits": visits})
	}); err != nil {
		return err
	}
	return h.Handle("GET /api/v1/hello", func(c *module.Caller, w http.ResponseWriter, r *http.Request) {
		var visits int
		err := db.QueryRowContext(r.Context(), `
			INSERT INTO hello_visits (profile_id, visits) VALUES (?, 1)
			ON CONFLICT (profile_id) DO UPDATE SET visits = visits + 1
			RETURNING visits`, c.ProfileID).Scan(&visits)
		if err != nil {
			h.Logger().Error("counting a visit failed", "err", err)
			module.Problem(w, module.Internal, "")
			return
		}
		module.JSON(w, map[string]any{"kind": c.Kind, "owner": c.Owner, "visits": visits})
	})
}

// ForgetProfiles deletes deleted profiles' visits.
func (Module) ForgetProfiles(ctx context.Context, db *sql.DB, profileIDs []string) error {
	for _, id := range profileIDs {
		if _, err := db.ExecContext(ctx, `DELETE FROM hello_visits WHERE profile_id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// ForgetAccount has nothing to do: visits belong to profiles.
func (Module) ForgetAccount(context.Context, *sql.DB, string) error { return nil }
