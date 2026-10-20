# Writing a module

Overfin is a small core, the gateway, with modules grafted onto it. The
watchlist is one; invites, a catalog and requests will be others. If the
official modules don't do what you need, you can write your own and build
Overfin with it.

## What a module can do

A module is a Go package that implements two methods:

```go
type Module interface {
	Info() module.Info          // name, dependencies, description, migrations
	Setup(h module.Host) error  // registers its routes
}
```

Through the `Host`, the core gives it:

- **Routes that act as the calling profile.** `h.Handle("GET /api/v1/…", fn)`.
  Before `fn` runs, the core has checked the login and found the account and
  the active profile (PIN, account expiry, required group…), and answered
  the caller itself if anything was missing. Catch-all patterns are refused.
- **The calling profile**, in `fn`'s `*module.Caller`: account, profile, its
  kind (`standard` or `kids`), whether it's the owner.
- **The media server as that profile, and only as it**: `c.Backend.Item` and
  `c.Backend.Items` return titles in the API's own format, with the
  profile's own limits applied (a kids rating ceiling, say). A module never
  gets the server's admin key.
- **Its own tables**: `h.DB()`, plus SQL migrations it ships in
  `Info().Migrations` (`001_….sql`, `002_….sql`…), applied once each, in order.
- **Errors in the API's standard form**: `module.Problem(w, module.NotFound, "")`.
- **A logger**: `h.Logger()`.
- **A part of "Download my data"**, by also implementing `module.Exporter`.

## A complete example

[examples/custom-build/hello](examples/custom-build/hello) greets the
calling profile and counts its visits in a table of its own. It's compiled
and tested with Overfin itself, so it's always current. The
[watchlist](modules/watchlist) is a real module built the same way, only on
this contract.

## Building Overfin with your module

Overfin's core is a Go package; your build is a few lines:

```go
package main

import (
	"github.com/C0PT3R/overfin"
	"github.com/C0PT3R/overfin/modules/watchlist"
	"example.org/mymodule"
)

func main() {
	overfin.Main(
		watchlist.Module{}, // the official modules you want
		mymodule.Module{},
	)
}
```

Build it like the official one (`go build`, or the Dockerfile with your
`main` package), then switch your module on in the control panel, like any
other. Nothing is ever loaded while Overfin runs.

## The rules that keep everyone safe

Modules live inside the gateway, so they follow its rules
([SECURITY.md](SECURITY.md)):

- **Act only as the calling profile.** Never return another profile's or
  another account's data. Use `c.Backend`, so the profile's limits apply.
- **Keep little, and keep it with the profile.** Name tables after your
  module, and give rows that belong to a profile a
  `REFERENCES profiles(id) ON DELETE CASCADE`, so deleting a profile or an
  account deletes them. Offer them in "Download my data".
- **Log nothing private.** No tokens, passwords, PINs, or what people watch.
- **Treat IDs as opaque.** Store the API's own IDs (`item_…`) as they are.
- **Never name the media server to users.** People see your site, not the
  software underneath.

## Responsibility

The official modules carry Overfin's guarantees. A module runs inside
Overfin with its full power: the contract keeps a well-behaved module safe,
but it's a convention, not a sandbox. A third-party module is its
developer's responsibility, and the operator's for running it, as with any
program.

The contract is the `module` package. It's a public promise: changes to it
are additive within a version, like the HTTP API's.
