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

## Milestone 1: a real site, end to end

The first goal is one complete website running on Overfin against a real
Jellyfin server, using only the public API.

1. Configuration reload without restart.
2. Storage: accounts, profiles, sessions and encrypted tokens.
3. Provisioning: Jellyfin users created on first login, with opaque names.
4. Jellyfin 12 adapter.
5. Library endpoints, with the image pipeline (decode, check, re-encode,
   cache, access checks).
6. Playback: capabilities, stream tickets, HLS playlist rewriting, progress,
   limits, transcode cleanup.
7. Profiles, PINs and step-up.
8. A minimal demo site in plain HTML and JavaScript.

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
