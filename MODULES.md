# Writing a module

Overfin is a small core, the gateway, with modules grafted onto it. The
watchlist, migration and invites are three; a catalog and requests will be
others. If the
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
- **Routes for visitors**, people signed in who have no account yet:
  `h.HandleVisitor("POST /api/v1/…", fn)`. The core has checked the login,
  that they may have an account, and that they have none. The module learns
  nothing about them; its `module.Visitor` can only have the core set their
  account up (`v.Adopt`, bringing an existing account of the media server
  over), and the core answers them itself.
- **Links people open from elsewhere**, an email or a message, before
  they've logged in: `h.HandleLink("GET /api/v1/…", fn)`. A fixed path that
  acts only on its query, reveals nothing, and only leads people on (a
  cookie, a redirect). Invitation links are one.
- **A say in how accounts are set up**, by implementing
  `module.SetupOffer`: while a module offers another way than a new account
  (bringing an old one, say), a first visit asks instead of creating one,
  and `account-setup-required` carries the offer (`"claim": "move"`).
- **The calling profile**, in `fn`'s `*module.Caller`: account, profile, its
  kind (`standard` or `kids`), whether it's the owner.
- **The media server as that profile, and only as it**: `c.Backend.Item` and
  `c.Backend.Items` return titles in the API's own format, with the
  profile's own limits applied (a kids rating ceiling, say). A module never
  gets the server's admin key.
- **A database of its own**: `h.DB()` is the module's own SQLite file
  (`modules/<name>.db`, next to the core's), with only its tables in it,
  created by the SQL migrations it ships in `Info().Migrations`
  (`001_….sql`, `002_….sql`…), applied once each, in order. The core's
  tables aren't there: what the module needs of the core's data comes
  through its host.
- **Word of deleted profiles and accounts**, by implementing
  `module.Forgetter`: the core tells every module that has the interface,
  switched on or not, and the module deletes what it holds about them (or,
  for what must stay counted, cuts the link).
- **Errors in the API's standard form**: `module.Problem(w, module.NotFound, "")`.
- **A logger**: `h.Logger()`.
- **A part of "Download my data"**, by also implementing `module.Exporter`.
- **A say in creating accounts**, by implementing `module.AccountGate`:
  before an account is created, refuse it (`&module.Refusal{Type:
  module.InvitationRequired}`) or shape it with an `AccountPlan` (its
  template, when its access ends, a label); once it exists,
  `AccountCreated` is called. Both run under the core's account-creation
  lock, so creations never race, and an error afterwards undoes the account
  entirely. An account always starts with its owner profile alone; its
  template only sets how many more the owner may create.
  `module.Adopting(ctx)` tells whether the visitor is bringing an existing
  account over rather than starting fresh. `h.Templates(ctx)` lists the
  account templates a plan can choose from.
- **Settings**, declared in `Info().Settings`: switches, or choices among
  values. The panel shows them under the module's switch, the operator
  changes them live, and `h.Setting("mode")` reads them; the module is set
  up again when they change.
- **The backend's adoption**, for its control panel routes:
  `h.Adoption()` counts the accounts brought over that are still linked to
  their old login, and moves them for good. It's nil when the backend can't
  bring accounts over.
- **The site's address**, `h.SiteURL()`, for links to it (empty when the
  core doesn't know it).
- **Control panel routes**: `h.HandleAdmin("GET /stats", fn)`, filed under
  `/admin/api/modules/<name>/stats` on the panel's private listener, where
  only signed-in admins reach them.
- **A tab in the control panel**: put a `panel.js` in `Info().AdminUI`. Its
  default export is `render(root, panel)`, where `panel.api(method, path,
  body)` calls the module's own admin routes and `panel.h` builds elements.
  The panel adds the tab while the module is switched on.

## A complete example

[examples/custom-build/hello](examples/custom-build/hello) greets the
calling profile, counts its visits in a table of its own, and shows the
totals in its own control panel tab. It's compiled
and tested with Overfin itself, so it's always current. The
[watchlist](modules/watchlist), [migration](modules/migration) and
[invites](modules/invites) are real modules built the same way, only on this
contract.

## Building Overfin with your module

The easy way is the Dockerfile: name your module, and it's built in with the
official ones, no Go needed:

```sh
docker build --build-arg MODULES="example.org/mymodule@v1.2.0" -t overfin .
```

`MODULES` takes several, separated by spaces. Each is a Go package path,
optionally with `@version`, whose package exports a `Module` type, as above.

To choose exactly what goes in, write the main package yourself.
Overfin's core is a Go package; your build is a few lines:

```go
package main

import (
	"github.com/C0PT3R/overfin-gateway"
	"github.com/C0PT3R/overfin-gateway/module"
	"github.com/C0PT3R/overfin-gateway/modules/watchlist"
	web "github.com/C0PT3R/overfin-web"
	"example.org/mymodule"
)

func main() {
	overfin.Run(overfin.Build{
		Modules: []module.Module{
			watchlist.Module{}, // the official modules you want
			mymodule.Module{},
		},
		Site: web.Files, // overfin-web; leave it out for no built-in site
	})
}
```

`overfin.Main(modules…)` is the short form: modules, and no site of its
own.

Build it with `go build`, then switch your module on in the control panel,
like any other. Nothing is ever loaded while Overfin runs.

## The core has authority

The core is like a kernel, and modules like programs: a module asks, through
its `Host`, and the core decides.

- **A module talks only to the core**: never to another module, never to
  the media server, never to the core's own data.
- **What several modules need is a core service**, offered through the host
  and governed by the core's rules, like the backend (`c.Backend`). If two
  modules would share something, it moves into the core.
- **`Needs` means "switch this on too"**, never "call into this".
- **The core can refuse**: guards, limits, and switching modules on and off
  are the core's decisions, not the module's.

Each module's database is its own, so a module can't reach the core's
data or another module's through it. Modules are still compiled into
Overfin, so this is authority by design, not a sandbox.

## The rules that keep everyone safe

Modules live inside the gateway, so they follow its rules
([SECURITY.md](SECURITY.md)):

- **Act only as the calling profile.** Never return another profile's or
  another account's data. Use `c.Backend`, so the profile's limits apply.
- **Keep little, and keep it with the profile.** Name tables after your
  module, key rows that belong to a profile or an account by its ID, and
  implement `module.Forgetter` so they're deleted with it. Offer them in
  "Download my data".
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
