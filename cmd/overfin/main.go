// Command overfin is the official build of Overfin: the core, the official
// modules, and overfin-web, Overfin's own web client.
package main

import (
	"github.com/C0PT3R/overfin"
	"github.com/C0PT3R/overfin/module"
	"github.com/C0PT3R/overfin/modules/invites"
	"github.com/C0PT3R/overfin/modules/migration"
	"github.com/C0PT3R/overfin/modules/watchlist"
	"github.com/C0PT3R/overfin/web"
)

func main() {
	overfin.Run(overfin.Build{
		Modules: []module.Module{
			invites.Module{},
			migration.Module{},
			watchlist.Module{},
		},
		Site: web.Files,
	})
}
