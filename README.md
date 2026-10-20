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

Compared with Jellyfin's own API, Overfin is more general and easier to use,
while exposing far less.

> **Status: early development.** Login, accounts, profiles, browsing and
> playback work against a real Jellyfin 12 server. It isn't ready for
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
recovering streams. Your site only decides how things look and behave. Should
a reload resume the film? Should playback follow you from laptop to phone?
What happens when another device takes over? Each is a few lines on your
side, not a fork of someone else's client.

Your components are native to your own stack: a React carousel, a Svelte
player, a Discord bot, a wall display.

## The JavaScript SDK

[sdk/js](sdk/js) is one ES module, with no dependencies and no build step,
plus TypeScript types. It wraps the API and carries what every client needs:
following the login, typed errors, paging, and a player that reports
progress, recovers streams and handles takeovers. A site on the SDK is
mostly screens.

## See it: the demo site

[examples/demo-site](examples/demo-site) is a small but complete website on
the SDK, in plain HTML, CSS and JavaScript, with no framework and no build
step:

- account setup, "Who's watching?" with PINs and kids profiles;
- home rows (continue watching, next up, latest), libraries with sorting,
  shuffle and infinite scroll, series → seasons → episodes;
- a player that tells Overfin what the browser can play, switches audio and
  subtitles, skips intros, reports progress, resumes on any device, recovers
  a stream the server freed, and offers "Continue here" after a takeover.

It's both a starting point and a reference for using the API.

## What works today

- **Login with any OpenID Connect provider** (Authentik, Authelia, Keycloak,
  Pocket ID…), or behind a reverse proxy's forward auth. Destructive actions
  need a fresh login (step-up).
- **Accounts and profiles.** Netflix-style profiles under one login, each with
  its own watch history. **Kids profiles** get a rating ceiling that Jellyfin
  itself enforces: titles above it, and unrated ones, are hidden everywhere,
  even from direct links. Optional PINs, with growing delays and a lock after
  repeated wrong guesses.
- **Your data, your call.** People can download everything held about them,
  with their watch history, and delete their account; the operator chooses
  whether the history goes too.
- **Bring existing users.** Optionally, people can bring their existing
  Jellyfin account and watch history by entering its username and password
  once (administrator accounts never). See
  [Migrating from Jellyfin](#migrating-from-jellyfin).
- **A clean data model.** Movies, series, episodes, music and more share one
  small core plus a kind-specific part. No 150-field objects, no file paths,
  no server internals.
- **Browsing.** Libraries, filters, sorting, a shuffle that survives a reload,
  cursor paging, "continue watching", "next up", recently added. Images are
  access-checked, re-encoded and stripped of metadata.
- **Playback that just works.** Clients describe what they can play; Overfin
  picks direct play, repackaging or transcoding. Stream URLs carry no Jellyfin
  token. Subtitles come as clean WebVTT, "skip intro" markers are included,
  and idle transcodes are stopped.
- **Gentle limits.** Streams per account (a new stream takes over the
  account's oldest by default), a server-wide transcode cap, a bitrate cap.
  Another account's stream is never cut off.

## Coming next

**Administration** is next: Overfin's own control panel, built in and never on
the internet, where every feature is switched on and off live, users are
managed (accounts, invites, expiry, limits), and who's watching is visible,
without Jellyfin's dashboard, where Overfin's users only appear under
pseudonyms. An optional Authentik connector will show real names and send
invites that include the Authentik sign-up. After that: a TMDB catalog (discover, curated lists, titles not in the library yet),
Seerr requests, profile transfers between accounts, queues, seek-bar
previews, and Jellyfin 10.11 support. The full list and
order are in [ROADMAP.md](ROADMAP.md).

## Plugins

Overfin always handles login, accounts, profiles and sessions. Everything
else is a plugin, and **every plugin is off by default**: an instance offers
only what its operator lists. A disabled plugin's routes don't exist at all.

| Plugin | Offers | Needs | Status |
| --- | --- | --- | --- |
| `library` | Libraries, items, filters and sorting, shuffle, continue watching, next up, recently added, images | | Available |
| `playback` | Direct play, repackaging or transcoding, token-free HLS, WebVTT subtitles, progress and resume, stream limits, skip-intro markers | `library` | Available |
| `catalog` | Discover lists, curated lists, titles not in the library yet | a TMDB key | Planned |
| `requests` | Requests, quotas, problem reports; kids profiles limited to their ceiling | Seerr | Planned |

Unknown names, planned plugins and missing dependencies stop Overfin at
startup rather than being ignored.

## Getting started

You need a **Jellyfin 12** server that isn't reachable from the internet, an
**OpenID Connect provider**, and a **reverse proxy** with HTTPS in front of
Overfin and your site.

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

(`[plugins]` here only seeds a new instance; afterwards, features are
switched in the [control panel](#control-panel).)

**4. Secrets**, as environment variables (or `NAME_FILE` pointing to a file,
for Docker secrets). They never go in the file:

| Variable | Meaning |
| --- | --- |
| `OVERFIN_JELLYFIN_ADMIN_KEY` | The Jellyfin API key from step 1 |
| `OVERFIN_SECRET_KEY` | Base64, at least 32 random bytes: `openssl rand -base64 32` |
| `OVERFIN_OIDC_CLIENT_SECRET` | The client secret from step 2 |
| `OVERFIN_CONNECTOR_TOKEN` | Only with `[connector]`: the identity provider API token |

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

To limit who gets an account, set `identity.required_group` to a group from
your identity provider. Without it, anyone who can log in there gets in.

## Migrating from Jellyfin

People already using your Jellyfin server keep their watch history:

1. Set `adoption.claim = true`.
2. Announce a date, and give everyone a login at your identity provider.
3. On their first visit, people choose "bring my existing account" and enter
   their old Jellyfin username and password, once.
4. On the date, close the old way to Jellyfin.

By default (`adoption.mode = "move"`) bringing an account is a move: the
Jellyfin account gets an opaque name and a new password nobody knows, so the
old login and Jellyfin apps stop working for that person at once. When you
overfin, you don't fin. For a gentler transition, `mode = "link"` lets moved
people keep using Jellyfin apps until the date; then run
`overfin -move-linked` once.

What people see is your site, under your name: they may never have heard of
Jellyfin, and Overfin never names it to them. Speak of "your previous
account" or your old service's name in your announcement and on your site.

Nobody loses anything by being late: their Jellyfin account and history
stay, and they can bring them whenever they get their login. Tell people who
watch through Jellyfin apps (TVs, phones) that those stop working; the
website works in any browser, and Overfin's own login for native apps is on
the roadmap.

## Control panel

Overfin has its own control panel, built in: every feature is switched on and
off there, and applies at once, and every account is managed there (labels,
access end dates, per-account stream limits, disabling, deleting, unlocking
PINs, signing devices out). It's served on a separate listener that
answers only private addresses, so it's never on the internet; admins sign in
through the identity provider with a fresh login, must belong to the admin
group, and stay signed in while they're active (30 idle minutes end it).

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

With Authentik, the optional connector shows who's behind each account
(username, name, email, whether they're still active there), read live and
never stored. Give it a token that can only view users:

```toml
[connector]
kind = "authentik"
url = "https://auth.example.org"
```

with `OVERFIN_CONNECTOR_TOKEN` set to an API token of an Authentik service
account whose role only allows viewing users.

To reach it from your LAN or VPN instead, give it an `https://` redirect URL
and a certificate (`tls_cert`, `tls_key`). The configuration file then only
holds what Overfin needs to start; feature settings in it are read once, to
fill a new instance.

## Login modes

The operator chooses, in `identity.mode`:

| Mode | For | Takes |
| --- | --- | --- |
| `oidc` (recommended) | Any OpenID Connect provider | The `[oidc]` section and one secret |
| `forwardauth` | Setups where a reverse proxy already handles login | The header names the proxy sets |
| `anonymous` | A private network with no login at all | Nothing |

In `oidc` mode, identity headers are ignored entirely, so there are none to
spoof. In `forwardauth` mode they're accepted only from `trusted_proxies`.
Step-up works in both modes whenever `[oidc]` is set; without it,
destructive actions are refused.

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

The full rules are in [SECURITY.md](SECURITY.md), and
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each one answers.

**Don't fin, overfin.** Jellyfin must not be reachable from the internet:
Overfin replaces public access to it, native Jellyfin apps included. It never
offers a route to Jellyfin's dashboard; operators reach that privately.

## API

The API is specified first, in [api/openapi.yaml](api/openapi.yaml)
(OpenAPI 3.1); its conventions, at the top of the file, apply to every
endpoint. Errors are RFC 9457 problem details with stable types, so clients
can branch on them.

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
need a restart (`listen`, `storage.path`, `jellyfin.url`, the secret key).
The log says which.

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
