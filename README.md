# Overfin

Overfin is your own streaming service on top of a Jellyfin server: a
server and its own apps, with your brand on all of it. The web app,
[overfin-web](https://github.com/overfin/web), comes first; mobile
and TV apps will follow. This repository is the server.

The server sits in front of Jellyfin, which browsers never reach through
it, and which can stay off the internet entirely (recommended). It holds everything that matters: who people
are (logins through your identity provider, accounts and profiles,
invitations), what each profile may see, limits, and a control panel to
manage it all. The apps hold the experience, and nothing else.

Every app, official or not, talks to the server through the same public,
documented API. That's what lets the official apps connect to any Overfin
server, someone else's included, and what lets anyone build a site or an
app of their own: since the server enforces the rules, a client of your own
can do everything the official ones do, with total freedom on looks and
feel.

```
browser ──▶ the site (overfin-web, or your own)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (private)
                          │
                          └──▶  your identity provider (login)
```

## Where it stands

Overfin is young. It started in October 2026, its releases are early
pre-releases (0.x), and it's developed by one person with AI assistance. In
practice:

- **It runs one real server**, every day, with its own web client
  (overfin-web), against Jellyfin 12. That is the extent of its real-world use.
- **Releases are pre-releases.** Each comes as a signed image for x86 and
  ARM (`ghcr.io/overfin/overfin`). The API, the settings and the database
  can still change between versions; the database migrates itself.
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
  optional PIN. Kids profiles have ceilings, one for films and one for
  series, on Overfin's own ratings from TMDB in the country the operator
  picks: titles above them, or with no rating there, are hidden everywhere,
  even from direct links, and can't be played. The account's owner can
  also give any profile only some libraries, and hide tags from it.
- People can download what's held about them and delete their account.

**Watching** (through the API, the SDK and overfin-web)

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

**By hand**, with your own reverse proxy and identity provider, you need a **Jellyfin 12** server, ideally not reachable from the internet
(see [SECURITY.md](SECURITY.md), Deployment, for what exposing both means), an
**OpenID Connect provider**, a **reverse proxy** with HTTPS, and **Docker**
(or Go 1.26) to build Overfin.

**1. A Jellyfin API key**, made in Jellyfin's dashboard. Overfin uses it only
to manage the Jellyfin users behind its profiles, never to browse or play.
Jellyfin's address and this key can also be left out of the configuration:
with the control panel switched on (`[admin]`), its setup page asks for
both, tests them and keeps them, the key sealed; the panel's Settings
change them later, for the same Jellyfin server.

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

```

Features that can be switched (invitations, migration) are switched in the
control panel.

**4. Secrets**, as environment variables (or `NAME_FILE` pointing to a file):

| Variable | |
| --- | --- |
| `OVERFIN_JELLYFIN_ADMIN_KEY` | The Jellyfin API key, with `[jellyfin]` (or neither, with the setup page) |
| `OVERFIN_SECRET_KEY` | Optional: without it, Overfin generates one on its first start, in `/secrets/overfin.key`. Either way, back it up: it seals what Overfin stores |
| `OVERFIN_OIDC_CLIENT_SECRET` | The client secret from step 2 |

**5. Run it** (the version from the
[releases](https://github.com/overfin/server/releases); `docker
build -t overfin .` builds it from source instead):

```sh
docker run -d --name overfin --network proxy \
  -v "$PWD/overfin.toml:/etc/overfin/overfin.toml:ro" \
  -v overfin-data:/data -v overfin-secrets:/secrets \
  -e OVERFIN_JELLYFIN_ADMIN_KEY -e OVERFIN_OIDC_CLIENT_SECRET \
  ghcr.io/overfin/overfin:0.2.0
```

**6. The reverse proxy** handles HTTPS and passes everything to Overfin,
which serves the API and the site. With Caddy:

```caddyfile
example.org {
	reverse_proxy overfin:8080
}
```

Open your domain and log in: you're on [overfin-web](https://github.com/overfin/web), Overfin's own
web client, served to logged-in visitors only.

**Your own client instead?** Set `[site] serve` to its folder: Overfin
serves it the same way, with the Content-Security-Policy you give it
(`csp`), and `spa_fallback = true` for single-page apps. With
`serve = "none"`, Overfin only answers `/api/`, and your proxy serves the
site, asking Overfin's gate first (`forward_auth` to `/api/v1/gate`) to keep
its pages private.

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

Switch migration on in the control panel, give everyone a login at your identity
provider, and announce a date. On their first visit, people choose to bring
their previous account and enter its old username and password once. By
default that's a move: the old login and Jellyfin apps stop working for them
at once. Mode "link" keeps the old login working until you press "Move
linked accounts" in its tab. Nobody loses anything by being late.

Overfin never names Jellyfin to the people using your site: speak of their
"previous account", or your old service's name.

## Features

Everything is built in. Accounts and profiles, the library, playback and the
watchlist are always on; the rest is switched on in the control panel.

| Feature | Offers | |
| --- | --- | --- |
| Library | Browsing, images | Always on |
| Playback | Watching, subtitles, limits | Always on |
| Watchlist | Each profile's watchlist | Always on |
| Invitations | Invitations and invite templates | Switch |
| Migration | Bringing Jellyfin accounts over | Switch |
| Catalog | The library's index, matched to TMDB | Always on |
| Discovery | Trending, popular and upcoming titles, from Seerr | Once Seerr is connected; not for kids profiles yet |

Discovery's data comes from TMDB, through Seerr: Overfin keeps it as
TMDB's terms allow (six months at most, deleted when Seerr is
disconnected), and overfin-web shows TMDB's attribution. TMDB's free
licence covers non-commercial use; a site that charges for access needs
TMDB's commercial terms.
| Requests | Seerr requests | Planned |

## Building your own client

- **The API**, specified in [api/openapi.yaml](api/openapi.yaml), with its
  conventions at the top. Errors are typed problem details.
- **The JavaScript SDK**, [sdk/js](sdk/js): one ES module, no dependencies,
  TypeScript types, and a player that handles progress, recovery and
  takeovers.
- **overfin-web**, in
  [its own repository](https://github.com/overfin/web): Overfin's own
  web app, in Svelte and TypeScript on the SDK, bundled by the official
  build and served by default. It's what the real server runs, and it uses
  nothing but the public API, like any client.

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

Useful to know: `overfin -check` tests the configuration and what it points
to (the secret key, Jellyfin, the identity provider) and says in plain words
what's wrong, changing nothing; `SIGHUP` reloads the configuration without dropping
playbacks; `-recover-tokens` gets you going again if the secret key is lost
(PINs and logins are reset, history is kept).

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE). overfin-web, which the official build bundles, includes
[hls.js](https://github.com/video-dev/hls.js), unmodified, under the Apache
License 2.0.

Overfin is an independent project, not affiliated with or endorsed by the
Jellyfin project, Seerr or TMDB.
