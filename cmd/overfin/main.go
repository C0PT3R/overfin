// Command overfin is the official build of Overfin: Overfin itself, and
// overfin-web, its own web client.
package main

import (
	"github.com/C0PT3R/overfin-server"
	web "github.com/C0PT3R/overfin-web"
)

func main() {
	overfin.Run(web.Files)
}
