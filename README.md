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

> **Status: early development.** The API is specified, the gateway is a
> skeleton. Nothing here is ready to run in production yet.

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

**Jellyfin must not be reachable from the internet.** Overfin replaces public
access to Jellyfin, including its native apps. It never offers a route to
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
go run ./cmd/overfin               # listens on :8080
curl localhost:8080/api/v1/health  # {"status":"ok"}
```

Docker:

```sh
docker build -t overfin .
docker run --rm -p 8080:8080 overfin
```

## Configuration

Only the listen address exists so far. A TOML configuration file is planned.

| Variable | Default | Meaning |
| --- | --- | --- |
| `OVERFIN_LISTEN` | `:8080` | Address the gateway listens on |

## AI-assisted development

Overfin is developed with the help of AI. Much of the code and documentation
is written together with Claude (Anthropic), and commits it contributed to
carry a `Co-Authored-By: Claude` trailer. Every change is reviewed by a human
maintainer, and the security-critical paths (login, sessions, stream
authorization) are read line by line.

## License

[MIT](LICENSE)
