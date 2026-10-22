// Command overfin is the official build of Overfin: Overfin itself, and
// overfin-web, its own web client.
package main

import (
	overfin "github.com/overfin/server"
	web "github.com/overfin/web"
)

func main() {
	overfin.Run(web.Files)
}
