# Overfin

**Build any client. Expose nothing.**

Overfin is a secure gateway between your Jellyfin server and the website or
app you build yourself. It sits next to Jellyfin and offers a small,
documented HTTP API under `/api/v1/`. Your site calls that API on its own
domain: the browser never talks to Jellyfin, never sees a Jellyfin token, and
can't reach anything Overfin doesn't explicitly offer.

```
browser ──▶ your site (any stack, even static HTML)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin
                          │
                          └──▶  your identity provider (login)
```

Overfin also manages the people who use your server: accounts, profiles,
invitations, limits, all from its own control panel. Jellyfin stays where it
belongs, private, doing what it does best: holding and playing your media.

> **Status: early development.** Everything below works against a real
> Jellyfin 12 server and runs a real site, but Overfin isn't ready for
> production yet. See [ROADMAP.md](ROADMAP.md).

## Why build your own?

Any custom client gives you creative freedom. What Overfin changes is the cost
of that freedom:

| Path | Freedom | Cost |
| --- | --- | --- |
| Customize Jellyfin (themes, plugins, injected scripts) | Limited to the slots Jellyfin offers | Breaks with Jellyfin updates |
| Build on Jellyfin's API | Total | A huge API to learn, tokens in the browser, Jellyfin exposed to the internet |
| Build on Overfin | Total | A small, documented API, with the security built in |

The hard and security-sensitive parts live in Overfin: choosing how to play a
file, keeping tokens out of the browser, profiles and their limits,
recovering streams. Your site only decides how things look and behave, with
components native to your own stack: a React carousel, a Svelte player, a
Discord bot, a wall display.

## What you get

**For the people watching**

- **Profiles** under one login, Netflix-style, each with its own watch
  history and optional PIN. **Kids profiles** get a rating ceiling that
  Jellyfin itself enforces: titles above it, and unrated ones, are hidden
  everywhere, even from direct links.
- **Playback that just works.** The browser says what it can play; Overfin
  picks direct play, repackaging or transcoding. Subtitles as clean WebVTT,
  "skip intro", resume on any device, streams that recover by themselves.
- **A watchlist** per profile: a movie, a whole show (its new episodes come
  along), a season or a single episode.
- **Their data, their call.** People can download everything held about
  them, watch history included, and delete their account.

**For the operator**

- **A control panel**, built in and never on the internet: every feature
  switched on and off live, every account managed (access end dates,
  templates, disabling, deleting, PIN unlocks, devices).
- **Account templates** saying what an account may do (streams at once,
  profiles). Editing one changes every account on it at once. A Default
  template exists from the start, so accounts can be opened right away.
- **Invitations**: links made from invite templates, each shaping the
  account it opens; optionally, accounts only by invitation.
- **Migration from Jellyfin**: people bring their existing account and its
  watch history, once, with its old username and password.
- **Real names in the panel**, read live from your identity provider through
  an optional connector (Authentik for now), never stored.
- **Gentle limits**: a new stream takes over the account's oldest, with a
  server-wide transcode cap and a bitrate cap. Another account's stream is
  never cut off.

**For developers**

- **A small API**, specified first in OpenAPI, with one clean data model for
  movies, series, music and more: no 150-field objects, no file paths, no
  server internals.
- **A JavaScript SDK** with a player that reports progress, recovers streams
  and handles takeovers.
- **A demo site** to start from.
- **Modules**: add features to Overfin itself, in Go, and build it with them.

## Getting started

You need a **Jellyfin 12** server that isn't reachable from the internet, an
**OpenID Connect provider** (Authentik, Authelia, Keycloak, Pocket ID…), and a
**reverse proxy** with HTTPS in front of Overfin and your site.

**1. A Jellyfin API key.** In Jellyfin's dashboard, create an API key. Overfin
uses it only to manage the Jellyfin users behind its profiles (create, limit,
move), never for browsing or playback.

**2. A client at your identity provider.** Register Overfin as a confidential
OAuth2/OpenID client with the redirect URL
`https://example.org/api/v1/auth/callback`. Allow refresh tokens (usually the
`offline_access` scope) so logins are re-checked every few minutes; without
them a login simply lasts 12 hours.

**3. The configuration**, `overfin.toml`. This is all it takes; everything
else has a default, and [overfin.example.toml](overfin.example.toml) lists
every setting:

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

`[plugins]` here only seeds a new instance. From then on, features live in
the database and are switched in the [control panel](#control-panel).

**4. Secrets**, as environment variables (or `NAME_FILE` pointing to a file,
for Docker secrets). They never go in the file:

| Variable | Meaning |
| --- | --- |
| `OVERFIN_JELLYFIN_ADMIN_KEY` | The Jellyfin API key from step 1 |
| `OVERFIN_SECRET_KEY` | Base64, at least 32 random bytes: `openssl rand -base64 32`. Keep a copy: it seals the tokens Overfin keeps |
| `OVERFIN_OIDC_CLIENT_SECRET` | The client secret from step 2 |
| `OVERFIN_CONNECTOR_TOKEN` | Only with `[connector]`: the identity provider's API token |

**5. Run it:**

```sh
docker build -t overfin .
docker run -d --name overfin --network proxy \
  -v "$PWD/overfin.toml:/etc/overfin/overfin.toml:ro" \
  -v overfin-data:/data \
  -e OVERFIN_JELLYFIN_ADMIN_KEY -e OVERFIN_SECRET_KEY -e OVERFIN_OIDC_CLIENT_SECRET \
  overfin
```

No port is published: only the reverse proxy reaches Overfin, over a Docker
network they share (`proxy` here).

**6. The reverse proxy.** Send `/api/*` to Overfin and serve your site for
everything else. Asking Overfin's gate first keeps the site's pages private
too: visitors who aren't logged in are sent to the login and brought back.
With Caddy:

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

Put the [demo site](examples/demo-site) in `/srv/site`, open your domain, log
in, and you're watching.

**Who gets an account?** Anyone who can log in at your identity provider,
unless you narrow it: set `identity.required_group` to a group there, or
switch on invitations only ([Invitations](#invitations)).

## Control panel

Overfin has its own control panel, on a separate listener that answers only
private addresses, so it's never on the internet. Admins sign in through the
identity provider with a fresh login, must belong to the admin group, and
stay signed in while they're active (30 idle minutes end it).

```toml
[admin]
listen = ":8081"
group = "overfin-admins"
redirect_url = "http://localhost:8081/admin/auth/callback"
```

Publish that port on the server's `127.0.0.1` only (for example
`ports: ["127.0.0.1:8081:8081"]`), register the redirect URL at your identity
provider, and reach the panel through an SSH tunnel:

```sh
ssh -L 8081:localhost:8081 your-server
# then open http://localhost:8081
```

To reach it from your LAN or VPN instead, give it an `https://` redirect URL
and a certificate (`tls_cert`, `tls_key`).

Its tabs:

- **Settings**: modules and their settings, limits, what happens when an
  account is deleted, privacy fields. Changes apply at once.
- **Users**: every account, with its profiles and devices; its template,
  when its access ends, disabling, deleting, PIN unlocks, signing devices
  out.
- **Templates**: the account templates.
- One tab per module that has one (invitations, migration…).

**Real names, with a connector.** Overfin itself only knows people by their
identity provider ID. With Authentik, the optional connector shows each
account's username, name, email and whether it's still active there, read
live and never stored:

```toml
[connector]
kind = "authentik"
url = "https://auth.example.org"
```

`OVERFIN_CONNECTOR_TOKEN` is a token made in Authentik's *Directory → Tokens
and App passwords*: intent **API Token** (an app password won't work), not
expiring, for a user with the permission **Can view User** and nothing
more. Copy that token's key, not another one's. Without a connector, give
accounts labels in the panel to tell them apart.

## Modules

Overfin is a small core, the gateway: login, accounts, profiles, sessions,
the control panel, and the guards around them. Everything else is a module,
and **every module is off by default**: an instance offers only what its
operator switches on, and a module that's off has no routes at all.

| Module | Offers | Needs | Status |
| --- | --- | --- | --- |
| `library` | Libraries, items, filters and sorting, shuffle, continue watching, next up, recently added, images | | Available |
| `playback` | Direct play, repackaging or transcoding, token-free HLS, WebVTT subtitles, progress and resume, stream limits, skip-intro markers | `library` | Available |
| `watchlist` | Each profile's own watchlist: movies, whole shows, seasons or single episodes | `library` | Available |
| `invites` | Invitation links from invite templates; optionally, accounts only by invitation | | Available |
| `migration` | People bring their previous Jellyfin account and its watch history | | Available |
| `catalog` | Discover lists, curated lists, titles not in the library yet | a TMDB key | Planned |
| `requests` | Requests, quotas, problem reports; kids profiles limited to their ceiling | Seerr | Planned |

(`library` and `playback` are switched like modules, but still live in the
core; they'll move onto the module contract like the others.)

**Your own modules.** Modules are compiled in, as with Caddy. If the official
ones don't do what you need, write your own in Go and build Overfin with it:
your build is a few lines. [MODULES.md](MODULES.md) explains the contract,
with a complete example. A module you write is yours to answer for, as with
any program you run.

## Invitations

With the `invites` module on, you invite people from its tab:

1. Make an invite template: which account template people get, how long
   their access lasts, how long the link works, and how many people can use
   it.
2. Make an invitation from it, for someone. Copy its link right away: it's
   shown only once, and Overfin keeps only a fingerprint of it.
3. They open the link, log in at your identity provider, and their account
   is set up as the invitation says.

Switch on "Accounts only by invitation" and nobody gets an account without
one, except people bringing their previous account over. People still need a
login at your identity provider: the invitation decides what they get here,
not who can log in there.

## Migrating from Jellyfin

People already using your Jellyfin server keep their watch history:

1. Switch the `migration` module on in the control panel.
2. Announce a date, and give everyone a login at your identity provider.
3. On their first visit, people choose "bring my existing account" and enter
   their old Jellyfin username and password, once.
4. On the date, close the old way to Jellyfin.

By default (mode "move") bringing an account is a move: the Jellyfin account
gets an opaque name and a new password nobody knows, so the old login and
Jellyfin apps stop working for that person at once. When you overfin, you
don't fin. For a gentler transition, mode "link" lets people keep using
Jellyfin apps until the date; then press "Move linked accounts" in the
module's tab.

What people see is your site, under your name: they may never have heard of
Jellyfin, and Overfin never names it to them. Speak of "your previous
account" or your old service's name in your announcement and on your site.

Nobody loses anything by being late: their Jellyfin account and history
stay, and they can bring them whenever they get their login. Tell people who
watch through Jellyfin apps (TVs, phones) that those stop working; the
website works in any browser, and Overfin's own login for native apps is on
the roadmap.

## Login modes

The operator chooses, in `identity.mode`:

| Mode | For | Takes |
| --- | --- | --- |
| `oidc` (recommended) | Any OpenID Connect provider | The `[oidc]` section and one secret |
| `forwardauth` | Setups where a reverse proxy already handles login | The header names the proxy sets |
| `anonymous` | A private network with no login at all | Nothing |

In `oidc` mode, identity headers are ignored entirely, so there are none to
spoof. In `forwardauth` mode they're accepted only from `trusted_proxies`.
Destructive actions (deleting a profile or the account, changing a PIN,
downloading one's data) need a fresh login, which works in both modes
whenever `[oidc]` is set; without it, they're refused.

## Security and privacy

Overfin treats Jellyfin as a trusted service serving untrusted data, and
every client as untrusted. Among its rules:

- No passthrough: every route is declared, and catch-all routes are refused
  at startup. Routes need a login unless explicitly public.
- Jellyfin tokens never reach the browser, not even inside playlists; they
  are encrypted at rest, and each profile has its own locked-down Jellyfin
  user.
- Only same-origin requests are accepted. Destructive actions need a fresh
  login; frequent sensitive ones need the PIN.
- Images are re-encoded and access-checked; metadata is served as plain text.
- No analytics, no telemetry, no stored IP addresses. Privacy-sensitive
  fields are off unless the operator turns them on. Watch history stays in
  Jellyfin.
- The control panel is never on the internet.

The full rules are in [SECURITY.md](SECURITY.md), and
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each one answers.

**Don't fin, overfin.** Jellyfin must not be reachable from the internet:
Overfin replaces public access to it, native Jellyfin apps included. It never
offers a route to Jellyfin's dashboard; operators reach that privately.

## Building a site

**The API** is specified first, in [api/openapi.yaml](api/openapi.yaml)
(OpenAPI 3.1); its conventions, at the top of the file, apply to every
endpoint. Errors are RFC 9457 problem details with stable types, so clients
can branch on them. Endpoints that are designed but not served yet are
marked `x-status: planned`, and a test keeps that true.

**The JavaScript SDK**, [sdk/js](sdk/js), is one ES module with no
dependencies and no build step, plus TypeScript types. It follows the login,
turns errors into typed exceptions, and comes with a player that tells
Overfin what the browser can play, reports progress, recovers streams and
handles takeovers. A site on the SDK is mostly screens.

**The demo site**, [examples/demo-site](examples/demo-site), is a small but
complete website on the SDK, in plain HTML, CSS and JavaScript, with no
framework and no build step:

- account setup (new, invited, or brought over), "Who's watching?" with
  PINs and kids profiles;
- home rows (continue watching, next up, latest), libraries with sorting,
  shuffle and infinite scroll, series → seasons → episodes, the watchlist;
- a player that switches audio and subtitles, skips intros, resumes on any
  device, recovers a stream the server freed, and offers "Continue here"
  after a takeover.

It's both a starting point and a reference for using the API.

## Coming next

Invitations that come with the identity provider's sign-up (one link for
both), a live view of who's watching, email (notices, and confirming
sensitive actions with an emailed code), then a TMDB catalog, Seerr
requests, seek-bar previews, a login for native apps, and Jellyfin 10.11
support. The full list and order are in [ROADMAP.md](ROADMAP.md).

## Compatibility

Overfin targets Jellyfin 12. Support for Jellyfin 10.11 is planned through a
separate adapter, so servers that haven't upgraded aren't left behind.

## Development

```sh
go test ./...
node --test sdk/js/overfin.test.js
go run ./cmd/overfin -config overfin.toml
```

Go, standard library first; two dependencies (a TOML parser and a pure-Go
SQLite). Settings are read from `/etc/overfin/overfin.toml` by default
(`-config` or `OVERFIN_CONFIG` to change it); unknown or invalid settings stop
Overfin at startup.

To apply changed settings without a restart, send Overfin `SIGHUP`
(`docker kill -s HUP overfin`): playbacks in progress carry on. A file that
doesn't load is refused and the running settings stay, as are changes that
need a restart (`listen`, `storage.path`, `jellyfin.url`, the admin and
connector sections, the secret key). The log says which.

If the secret key is lost for good, start Overfin once with
`-recover-tokens` and a new key: profiles keep their history, while PINs
and logins are reset.

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE). The demo site includes [hls.js](https://github.com/video-dev/hls.js),
unmodified, under the Apache License 2.0.

Overfin is an independent project. It is not affiliated with or endorsed by
the Jellyfin project, Seerr or TMDB.
