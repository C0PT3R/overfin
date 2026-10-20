// Command custom-build is an operator's own Overfin: the official modules
// plus their own (here, the hello example). Build it like the official one:
//
//	go build -o overfin ./examples/custom-build
package main

import (
	"github.com/C0PT3R/overfin"
	"github.com/C0PT3R/overfin/examples/custom-build/hello"
	"github.com/C0PT3R/overfin/modules/watchlist"
)

func main() {
	overfin.Main(
		watchlist.Module{},
		hello.Module{},
	)
}
