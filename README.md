# Overfin

Overfin sits in front of a Jellyfin server and lets you run your own website
for it. Your site talks to Overfin's small API; Jellyfin stays private,
never exposed to the internet or to browsers. Overfin also takes care of the
people using your server: logins through your identity provider, accounts
and profiles, invitations, and a control panel to manage it all.

```
browser ──▶ your site (any stack, even static HTML)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (private)
                          │
                          └──▶  your identity provider (login)
```

## Where it stands

Overfin is young. It started in October 2026, has no release yet, and is
developed by one person with AI assistance. In practice:

- **It runs one real server**, every day, with the demo site in this
  repository, against Jellyfin 12. That is the extent of its real-world use.
- **There are no releases or prebuilt images.** You build it from source.
  The API, the settings and the database can still change between commits;
  the database migrates itself.
- **Only Authentik has been tried** as the identity provider. Any OpenID
  Connect provider should work, but none other has been tested.
- **Browser only.** There's no login for native apps yet, and Jellyfin's own
  apps don't go through Overfin, so people who move to it watch in a
  browser.
- **Jellyfin 12 only.**
- **Its security rules are written down and tested, but nobody else has
  reviewed them yet.**

Try it, read it, build on it; don't make something you depend on rely on it
yet.

## What works

**Logging in and accounts**

- Login through your OpenID Connect provider (or a reverse proxy's forward
  auth), re-checked every few minutes. Deleting things, changing a PIN or
  downloading one's data asks for a fresh login.
- Accounts with Netflix-style profiles, each with its own watch history and
  optional PIN. Kids profiles have a rating ceiling enforced by Jellyfin
  itself, so titles above it are hidden everywhere, even from direct links.
- People can download what's held about them and delete their account.

**Watching** (through the API, the SDK and the demo site)

- Browsing: libraries, filters, sorting, shuffle, continue watching, next
  up, recently added, series → seasons → episodes, people.
- Playback: Overfin picks direct play, repackaging or transcoding from what
  the browser says it can play. WebVTT subtitles, audio tracks, "skip
  intro", resume on any device, streams that recover by themselves, one
  device taking over from another.
- A watchlist per profile: movies, whole shows, seasons or single episodes.

**Running the server**

- A control panel on a private address only: feature switches that apply at
  once, every account with its profiles and devices, account templates
  (streams and profiles allowed), access end dates, disabling, deleting,
  PIN unlocks.
- Invitations made from invite templates, with an optional "accounts only by
  invitation".
- Migration: people bring their existing Jellyfin account and its history,
  once, with its username and password.
- Real names in the panel, read live from Authentik through an optional
  connector.
- Stream limits per account, a server-wide transcode cap, a bitrate cap.

## What's missing

The main gaps, roughly in the order they're planned (details in
[ROADMAP.md](ROADMAP.md)):

- Invitations that include the identity provider's sign-up (today, invited
  people need a login there first).
- A live view of who's watching.
- Email: notices, and confirming sensitive actions with an emailed code.
- A login for native apps, and SDKs other than JavaScript.
- Seek-bar previews, search, favorites, a TMDB catalog, Seerr requests.
- Jellyfin 10.11 support.

The API specification also describes endpoints that aren't served yet; they
are marked `x-status: planned`.

## Trying it

**The quickest way** is [deploy/](deploy): Overfin, Caddy and Authentik in
one compose file, with Authentik set up for Overfin by a blueprint. A script
asks a few questions and generates every secret.

**By hand**, with your own reverse proxy and identity provider, you need a **Jellyfin 12** server that isn't reachable from the internet, an
**OpenID Connect provider**, a **reverse proxy** with HTTPS, and **Docker**
(or Go 1.26) to build Overfin.

**1. A Jellyfin API key**, made in Jellyfin's dashboard. Overfin uses it only
to manage the Jellyfin users behind its profiles, never to browse or play.

**2. A client at your identity provider**: a confidential OpenID client with
the redirect URL `https://example.org/api/v1/auth/callback`, allowed to issue
refresh tokens (usually the `offline_access` scope).

**3. `overfin.toml`**, the least it needs
([overfin.example.toml](overfin.example.toml) has every setting):

```toml
[jellyfin]
url = "http://192.0.2.10:8096"

[identity]
mode = "oidc"
trusted_proxies = ["198.51.100.7"]   # your reverse proxy

[oidc]
issuer = "https://auth.example.org/application/o/site/"
client_id = "overfin"
redirect_url = "https://example.org/api/v1/auth/callback"

[plugins]
enable = ["library", "playback"]
```

`[plugins]` only seeds a new instance; after that, features are switched in
the control panel.

**4. Secrets**, as environment variables (or `NAME_FILE` pointing to a file):

| Variable | |
| --- | --- |
| `OVERFIN_JELLYFIN_ADMIN_KEY` | The Jellyfin API key |
| `OVERFIN_SECRET_KEY` | `openssl rand -base64 32`, kept safe: it seals what Overfin stores |
| `OVERFIN_OIDC_CLIENT_SECRET` | The client secret from step 2 |

**5. Build and run:**

```sh
docker build -t overfin .
docker run -d --name overfin --network proxy \
  -v "$PWD/overfin.toml:/etc/overfin/overfin.toml:ro" \
  -v overfin-data:/data \
  -e OVERFIN_JELLYFIN_ADMIN_KEY -e OVERFIN_SECRET_KEY -e OVERFIN_OIDC_CLIENT_SECRET \
  overfin
```

**6. The reverse proxy** sends `/api/*` to Overfin and asks Overfin's gate
before serving the site, so its pages are private too. With Caddy:

```caddyfile
example.org {
	handle /api/* {
		reverse_proxy overfin:8080
	}
	handle {
		forward_auth overfin:8080 {
			uri /api/v1/gate
		}
		root * /srv/site
		file_server
	}
}
```

Put the [demo site](examples/demo-site) in `/srv/site`, open your domain and
log in.

Anyone who can log in at your identity provider gets an account, unless you
set `identity.required_group`, or switch invitations on and make them
required.

### The control panel

Add an `[admin]` section and publish its port on the server's `127.0.0.1`
only; reach it through an SSH tunnel (`ssh -L 8081:localhost:8081 server`,
then `http://localhost:8081`), or on your LAN or VPN over HTTPS:

```toml
[admin]
listen = ":8081"
group = "overfin-admins"   # who may sign in, at your identity provider
redirect_url = "http://localhost:8081/admin/auth/callback"
```

For real names with Authentik, add a `[connector]` (`kind = "authentik"`,
`url = "https://auth.example.org"`) and set `OVERFIN_CONNECTOR_TOKEN` to an
Authentik token of intent **API Token** (not an app password), for a user
allowed only to **view users**.

### Moving people over from Jellyfin

Switch the `migration` module on, give everyone a login at your identity
provider, and announce a date. On their first visit, people choose to bring
their previous account and enter its old username and password once. By
default that's a move: the old login and Jellyfin apps stop working for them
at once. Mode "link" keeps the old login working until you press "Move
linked accounts" in the module's tab. Nobody loses anything by being late.

Overfin never names Jellyfin to the people using your site: speak of their
"previous account", or your old service's name.

## Modules

Overfin is a core (logins, accounts, profiles, sessions, the control panel
and the guards around them) with features grafted on as modules, Caddy-style:
compiled in, each off until switched on.

| Module | Offers | Status |
| --- | --- | --- |
| `library` | Browsing, images | Works; still part of the core |
| `playback` | Watching, subtitles, limits (needs `library`) | Works; still part of the core |
| `watchlist` | Each profile's watchlist (needs `library`) | Works |
| `invites` | Invitations and invite templates | Works |
| `migration` | Bringing Jellyfin accounts over | Works |
| `catalog` | TMDB discovery and lists | Planned |
| `requests` | Seerr requests | Planned |

You can write your own and build Overfin with it in one command
(`docker build --build-arg MODULES="example.org/mymodule" -t overfin .`):
[MODULES.md](MODULES.md) describes the contract, with a working example. A
module you add is your responsibility, as with any code you run.

## Building on it

- **The API**, specified in [api/openapi.yaml](api/openapi.yaml), with its
  conventions at the top. Errors are typed problem details.
- **The JavaScript SDK**, [sdk/js](sdk/js): one ES module, no dependencies,
  TypeScript types, and a player that handles progress, recovery and
  takeovers.
- **The demo site**, [examples/demo-site](examples/demo-site): plain HTML,
  CSS and JavaScript on the SDK, no build step. It's what the real server
  runs, and the reference for using the API.

## Security

Overfin treats every browser as untrusted and Jellyfin's data as untrusted
input. Among its rules: every route is declared (no passthrough to
Jellyfin), Jellyfin tokens never reach the browser and are encrypted at
rest, each profile has its own locked-down Jellyfin user, images are
re-encoded, nothing is collected for analytics, and the control panel is
never on the internet.

The rules are in [SECURITY.md](SECURITY.md), the threats they answer in
[THREAT_MODEL.md](THREAT_MODEL.md). Remember they haven't been reviewed by
anyone outside the project yet.

## Development

```sh
go test ./...
node --test sdk/js/overfin.test.js
go run ./cmd/overfin -config overfin.toml
```

Go with the standard library and two dependencies (a TOML parser and a
pure-Go SQLite). CI runs formatting, vet, staticcheck, govulncheck, the tests
with the race detector, and a check that the API specification matches the
routes.

Useful to know: `SIGHUP` reloads the configuration without dropping
playbacks; `-recover-tokens` gets you going again if the secret key is lost
(PINs and logins are reset, history is kept).

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE). The demo site includes [hls.js](https://github.com/video-dev/hls.js),
unmodified, under the Apache License 2.0.

Overfin is an independent project, not affiliated with or endorsed by the
Jellyfin project, Seerr or TMDB.
