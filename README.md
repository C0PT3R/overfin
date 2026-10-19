# Overfin

**Build any client. Expose nothing.**

Overfin is a secure gateway between a Jellyfin server and the website or app
you build yourself. It sits next to Jellyfin and offers a small, documented,
stable HTTP API under `/api/v1/`. Your site calls that API on its own domain;
the browser never talks to Jellyfin, never sees a Jellyfin token, and can't
reach anything Overfin doesn't explicitly offer.

Compared with Jellyfin's own API, Overfin is more general and more usable,
while exposing less.

```
browser ──▶ your website (any stack, even static HTML)
   └──▶ /api/v1/*  ──▶  Overfin  ──▶  Jellyfin (+ TMDB, Seerr)
```

## Why build your own?

Any custom client gives you creative freedom. What Overfin changes is the cost
of that freedom:

| Path | Freedom | Cost |
| --- | --- | --- |
| Customize Jellyfin (themes, plugins, injected scripts) | Limited to the slots Jellyfin offers | Breaks with Jellyfin updates |
| Build on Jellyfin's API | Total | A huge API to learn, tokens in the browser, Jellyfin exposed to the internet |
| Build on Overfin | Total | A small, documented API, with the security built in |

Overfin doesn't make creativity possible; it makes it cheap and safe, so
building your own becomes the obvious path instead of the brave one. Your
components are native to your own stack: a React carousel, a Svelte player, a
Discord bot, a wall display. They're not plugins squeezed into someone else's
UI.

> **Status: early development.** Accounts, profiles, browsing and playback
> work against a real Jellyfin server; the rest of the API is specified but
> not built yet. Nothing here is ready to run in production yet. See
> [ROADMAP.md](ROADMAP.md) for what's done and what comes next.

## What it offers

- **A clean data model of its own.** Movies, series, music, books, photos and
  live TV share one small core plus a kind-specific part. No 150-field
  objects, no file paths, no server internals.
- **Profiles under one login.** Netflix-style profiles, including kids
  profiles whose limits are enforced by Jellyfin itself, optional PINs, and
  profiles that can move to another account.
- **Playback that just works.** Clients describe what they can play, and
  Overfin picks direct play, repackaging or transcoding. Stream URLs never
  carry Jellyfin tokens, a reload restores playback and the queue, and
  "Skip intro" markers and seek-bar previews are included.
- **Gentle limits.** A new stream takes over the account's oldest one, and a
  busy server lowers quality before refusing. Another user's stream is never
  cut off.
- **An optional TMDB catalog.** Discover lists, operator-curated lists
  ("Christmas movies we have"), and titles that aren't in the library yet.
- **Optional Seerr requests.** Requests, quotas and problem reports, with kids
  profiles limited to titles within their rating ceiling.
- **Operator choice.** Features, identity mode, user mapping, limits and every
  privacy-sensitive field are set in the configuration. Sensitive data is off
  by default.

## Plugins

Overfin always manages accounts, profiles and sessions. Everything else is a
plugin, and **every plugin is off by default**: an instance offers only what
its operator lists in `[plugins] enable`. A disabled plugin's routes don't
exist at all; they answer `not-found` like any unknown address.

| Plugin | Offers | Needs | Status |
| --- | --- | --- | --- |
| *(core, always on)* | Accounts, adoption of existing Jellyfin users (separately opt-in), profiles including kids profiles and PINs, sessions and devices, logout | Jellyfin | Available |
| `library` | Libraries, items with filters and sorting, random order, "continue watching", "next up", recently added, posters and other images (re-encoded) | Jellyfin | Available |
| `playback` | Direct play, repackaging or transcoding chosen from the client's capabilities, HLS without tokens, subtitles as WebVTT, progress and resume, stream limits, "skip intro" markers | `library` | Available |
| `catalog` | Discover lists, operator-curated lists, titles not in the library yet | TMDB key | Planned |
| `requests` | Requests, quotas and problem reports, kids profiles limited to their rating ceiling | Seerr | Planned |

```toml
[plugins]
enable = ["library", "playback"]
```

Unknown names, planned plugins and missing dependencies stop Overfin at
startup rather than being ignored.

## Security and privacy

Overfin treats Jellyfin, Seerr and TMDB as trusted services serving
untrusted data, and treats every client as untrusted. Among its rules:

- No passthrough: every route is declared, and catch-all routes are refused at
  startup.
- Upstream tokens never reach the browser, not even inside playlists.
- Images are re-encoded and access-checked; metadata is served as plain text.
- Only same-origin requests are accepted; destructive actions need a fresh
  login.
- No analytics, no telemetry, no stored IP addresses. Watch history stays in
  Jellyfin.

The full rules are in [SECURITY.md](SECURITY.md), and
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each one answers.

**Don't fin, overfin.** Jellyfin must not be reachable from the internet.
Overfin replaces public access to Jellyfin, including its native apps. It never offers a route to
Jellyfin's dashboard; operators reach that privately.

## API

The API is defined spec-first in [api/openapi.yaml](api/openapi.yaml)
(OpenAPI 3.1). Its conventions, at the top of the file, apply to every
endpoint. A TypeScript client SDK will be generated from it.

## Compatibility

Overfin targets Jellyfin 12.x. Support for Jellyfin 10.11 is planned through a
separate adapter, so servers that haven't upgraded aren't left behind.

## Build and run

```sh
go test ./...

export OVERFIN_JELLYFIN_ADMIN_KEY=your-jellyfin-api-key
export OVERFIN_SECRET_KEY="$(openssl rand -base64 32)"
go run ./cmd/overfin -config overfin.example.toml
curl localhost:8080/api/v1/health  # {"status":"ok"}
```

Docker:

```sh
docker build -t overfin .
docker run --rm -p 8080:8080 \
  -v "$PWD/overfin.toml:/etc/overfin/overfin.toml:ro" \
  -e OVERFIN_JELLYFIN_ADMIN_KEY -e OVERFIN_SECRET_KEY \
  overfin
```

## Configuration

Settings live in a TOML file, by default `/etc/overfin/overfin.toml` (change it
with `-config` or `OVERFIN_CONFIG`). See
[overfin.example.toml](overfin.example.toml) for every setting. Unknown or
invalid settings stop Overfin at startup.

Secrets never go in the file. Set them as environment variables, or as
`NAME_FILE` pointing to a file (for Docker secrets):

| Variable | Meaning |
| --- | --- |
| `OVERFIN_JELLYFIN_ADMIN_KEY` | Jellyfin API key, used only to create users |
| `OVERFIN_SECRET_KEY` | Base64, at least 32 random bytes; encrypts tokens and signs cursors |

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE)

Overfin is an independent project. It is not affiliated with or endorsed by
the Jellyfin project, Seerr or TMDB.
