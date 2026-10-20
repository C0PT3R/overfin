// Command overfin is the official build of Overfin: the core, the official
// modules, and overfin-web, Overfin's own web client.
package main

import (
	"github.com/C0PT3R/overfin-gateway"
	"github.com/C0PT3R/overfin-gateway/module"
	"github.com/C0PT3R/overfin-gateway/modules/invites"
	"github.com/C0PT3R/overfin-gateway/modules/migration"
	"github.com/C0PT3R/overfin-gateway/modules/watchlist"
	web "github.com/C0PT3R/overfin-web"
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
