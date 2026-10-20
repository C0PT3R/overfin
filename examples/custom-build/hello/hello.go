// Package hello is a tiny example module: one route that greets the calling
// profile and counts its visits in a table of its own. See MODULES.md.
package hello

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/C0PT3R/overfin/module"
)

//go:embed migrations/*.sql
var files embed.FS

// Module is the hello module.
type Module struct{}

func (Module) Info() module.Info {
	migrations, _ := fs.Sub(files, "migrations")
	return module.Info{
		Name:        "hello",
		Description: "An example module that greets the calling profile",
		Migrations:  migrations,
	}
}

func (Module) Setup(h module.Host) error {
	db := h.DB()
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
