// Command overfin is the official build of Overfin: the core with the
// official modules.
package main

import (
	"github.com/C0PT3R/overfin"
	"github.com/C0PT3R/overfin/modules/migration"
	"github.com/C0PT3R/overfin/modules/watchlist"
)

func main() {
	overfin.Main(
		migration.Module{},
		watchlist.Module{},
	)
}
