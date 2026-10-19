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
- Identity: Overfin's own OpenID Connect login (one provider for login and
  step-up, logins re-checked by refresh token, a gate for the reverse proxy
  to hide the site's pages), or forward-auth from trusted proxies; client IP
  from the proxy's own forwarded header; routes protected by default.
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
- Streaming: direct play with seeking (Range), HLS playlists parsed and
  rewritten so no Jellyfin URL or token reaches the client, segments
  mapped back server-side, subtitles rebuilt as plain WebVTT; tickets work
  only for the profile that started the playback.
- Version names replaced by neutral descriptions unless the operator
  exposes them (`[privacy] expose`).
- Playback lifecycle: progress reported to Jellyfin, stop that ends the
  transcode, idle playbacks ended after two minutes by a janitor, stream
  limits that replace the account's oldest playback (or refuse), a total
  transcode limit, and restoring the current playback after a reload.
- Sessions and profiles: an Overfin session cookie per device, honored only
  for the account it was created for; additional (standard) profiles with
  their own Jellyfin users; "Who's watching?" switching; session listing,
  ending and logout; stream tickets bound to their session.

## Milestone 1: a real site, end to end

The first goal is one complete website running on Overfin against a real
Jellyfin server, using only the public API.

1. Configuration reload without restart.
2. Adoption extras: optional matching by name or email (operator opt-in),
   and pseudonymizing adopted accounts once the old access path is retired.
3. ~~Kids profiles (rating ceilings enforced by Jellyfin) and PINs.~~ Done.
4. ~~Step-up for destructive actions.~~ Done, over OpenID Connect: changing
   and removing PINs, deleting profiles, and lifting PIN locks. Left: account
   deletion and data export, and making the PIN lock threshold an operator
   setting.
5. ~~A minimal demo site in plain HTML and JavaScript.~~ Done:
   [examples/demo-site](examples/demo-site).

## Milestone 2: administration

A private admin side, to run an instance without touching Jellyfin's
dashboard, where Overfin's users only appear under pseudonyms. Its own
listener, never on the internet (SECURITY.md rule 58), with its own API and
SDK part, so operators build the admin page they want.

1. The admin listener: private addresses only, admin group, step-up for
   every change, an admin part of the JavaScript SDK.
2. Users: list and search accounts with their profiles, disable, re-enable,
   delete (with their Jellyfin users), account expiry, per-account limits,
   PIN-lock resets, sessions, adoption of existing Jellyfin users, labels.
3. Invites: links with an expiry, a number of uses and a template (profiles,
   kids limits, stream limit, account expiry); optionally, accounts only by
   invite.
4. Live view: who's watching, method and bitrate, transcode load; stopping a
   stream; history and activity read live from Jellyfin, never stored.
5. Identity provider connectors, optional: Authentik first (create users,
   groups, password-reset emails, disable), so user management needs no
   trip to the provider's own admin pages. Their tokens are used only by the
   admin side.
6. Notices, off by default: invite used, account expiring (email, webhook),
   carrying account events only, never viewing activity.

## Designed, to implement later

- TMDB catalog plugin: discover, catalog titles, curated lists.
- Seerr requests plugin: requests, quotas, problem reports.
- Profile avatars, profile transfers, data export.
- Trickplay, lyrics, skip markers, bitrate test.

## Planned, not designed yet

- **Playback extras:** changing tracks or quality mid-playback (`PATCH`),
  queues and "advance", and degrading (direct play, lower quality) before
  refusing when the server is busy.

- **Native-app login.** Today's login works in browsers (and webview apps).
  Native apps, including third-party ones, need:
  - signing in through the system browser (OAuth with PKCE), and on TVs a
    short code approved from a phone;
  - an Overfin-issued token sent in an `Authorization` header, revocable and
    listed among the account's devices, never a Jellyfin token;
  - a way through the reverse proxy for token requests, which Overfin then
    checks itself.
- **More SDKs** beyond JavaScript (done: [sdk/js](sdk/js), hand-written,
  with TypeScript types), for native apps.
- **Jellyfin 10.11 adapter**, so servers that haven't upgraded aren't left
  behind.
- **Live updates** (server-sent events): new items, playback taken over,
  request status.
- **Casting** to Chromecast and similar devices, using stream tickets.
- **Downloads** for offline viewing in apps.
- **Jellyfin companion plugin**, only where the Jellyfin API falls short.
- **A full reference website**, possibly growing out of the demo site.

## Companion projects

Features that need data Overfin doesn't keep (SECURITY.md rule 46) belong in
separate projects, with their own storage and their own rules for it. The
first idea: a reusable **social service** (friends, posts, lists, comments),
built on Overfin without changing it:

- It logs users in with the same identity provider, so it knows the same
  user IDs without asking Overfin.
- It refers to titles by Overfin's item IDs and stores no metadata; sites
  fetch titles and posters from Overfin.
- **Visibility:** before showing anything that names a title, the site looks
  the item up through Overfin as the viewer, and hides it on `not-found`. A
  kids profile, or a viewer without access to a library, never learns of a
  title through a friend; Overfin's per-profile limits protect the feed too.
- Overfin's part: document that item IDs are stable while Jellyfin keeps the
  item, so they're safe to store elsewhere.

## Open questions

- Step-up checks the login time in the ID token rather than trusting
  `prompt=login`. Confirm each supported identity provider updates it on a
  forced login (Authentik first).
- How long do TMDB's terms allow catalog data to be cached?
- Configuration format details: which settings exist, and their defaults.
