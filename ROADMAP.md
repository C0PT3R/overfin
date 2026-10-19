# Roadmap

Where Overfin stands and what comes next. Items move from **planned** to
**designed** once they are specified in [api/openapi.yaml](api/openapi.yaml)
and covered by [SECURITY.md](SECURITY.md), and from **designed** to **done**
once they are implemented and tested.

## Done

- Gateway skeleton: HTTP server, security headers, body limits, panic
  recovery, graceful shutdown.
- Route registrar that only accepts fixed routes under `/api/v1/` and refuses
  catch-all patterns at startup.
- Security rules and threat model.
- First complete draft of the API specification.
- Configuration file (TOML) with strict decoding and validation; secrets from
  the environment or Docker secret files.
- Error responses as RFC 9457 problem details, from one registry of error
  types.
- Key derivation (HKDF): one working key per purpose from the secret key.
- Signed cursors, bound to their query.
- Identity: forward-auth from trusted proxies only, client IP from the
  proxy's own forwarded header, routes protected by default.
- Storage (SQLite, pure Go): accounts, profiles and sessions, with upstream
  tokens sealed (AES-256-GCM, bound to their row) and session tokens stored
  only as hashes; embedded migrations.
- Jellyfin 12 adapter: user creation, policy lock-down, login; no redirects,
  bounded responses, validated IDs.
- Provisioning on first visit (`GET /api/v1/me`): opaque Jellyfin users with
  locked-down policies, required group, rate limit, rollback on failure.
- Same-origin requests only, and JSON for every change (CSRF protection).
- Adopting an existing Jellyfin account by its password (`POST /me/claim`),
  off by default: administrators refused, attempts limited.
- Library browsing: libraries, item queries with filters and signed cursors,
  item details, resume, next up and latest, translated into Overfin's own
  model with plain-text metadata.
- Image pipeline: per-profile access checks (also for cached images),
  header-checked dimensions, decoding limited to JPEG and PNG, re-encoding
  that strips all metadata, bounded memory cache and concurrency.
- Repeatable random order: `sort=random` with a seed, shuffled by Overfin so
  pages never overlap and a reload shows the same order.
- Starting playback (`POST /playback`): capabilities translated into a
  Jellyfin device profile, the direct/remux/transcode decision, stream
  tickets, tracks, versions and skip markers; remote sources and unexpected
  upstream URLs refused.

## Milestone 1: a real site, end to end

The first goal is one complete website running on Overfin against a real
Jellyfin server, using only the public API.

1. Configuration reload without restart.
2. Adoption extras: optional matching by name or email (operator opt-in),
   and pseudonymizing adopted accounts once the old access path is retired.
3. Playback: capabilities, stream tickets, HLS playlist rewriting, progress,
   limits, transcode cleanup.
4. Profiles, PINs and step-up.
5. A minimal demo site in plain HTML and JavaScript.

## Designed, to implement after milestone 1

- TMDB catalog plugin: discover, catalog titles, curated lists.
- Seerr requests plugin: requests, quotas, problem reports.
- OIDC identity plugin, for setups without forward-auth.
- Profile avatars, profile transfers, data export.
- Trickplay, lyrics, skip markers, bitrate test.

## Planned, not designed yet

- **Native-app login.** Today's login works in browsers (and webview apps).
  Native apps, including third-party ones, need:
  - signing in through the system browser (OAuth with PKCE), and on TVs a
    short code approved from a phone;
  - an Overfin-issued token sent in an `Authorization` header, revocable and
    listed among the account's devices, never a Jellyfin token;
  - a way through the reverse proxy for token requests, which Overfin then
    checks itself.
- **TypeScript SDK**, generated from the API specification, with automatic
  device capability detection.
- **Jellyfin 10.11 adapter**, so servers that haven't upgraded aren't left
  behind.
- **Overfin's own administration**, on a separate listener for the
  operator's private network only (SECURITY.md rule 58).
- **Live updates** (server-sent events): new items, playback taken over,
  request status.
- **Casting** to Chromecast and similar devices, using stream tickets.
- **Downloads** for offline viewing in apps.
- **Social features**, with each user's consent on top of the operator's:
  watch parties, shared playlists, recommendations between friends.
- **Jellyfin companion plugin**, only where the Jellyfin API falls short.
- **A full reference website**, possibly growing out of the demo site.

## Open questions

- Can each supported identity provider force a fresh login for step-up when
  used through forward-auth?
- How long do TMDB's terms allow catalog data to be cached?
- Configuration format details: which settings exist, and their defaults.
