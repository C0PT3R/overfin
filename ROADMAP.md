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

## Milestone 1: a real site, end to end (done)

The first goal is one complete website running on Overfin against a real
Jellyfin server, using only the public API.

1. ~~Configuration reload without restart.~~ Done: SIGHUP.
2. ~~Adoption extras.~~ Done: claiming is a one-way move by default
   (opaque name, lock-down, new password), or "link" for a transition, moved
   later with `overfin -move-linked`. No matching by name or email: only the
   username and password prove an account.
3. ~~Kids profiles (rating ceilings enforced by Jellyfin) and PINs.~~ Done.
4. ~~Step-up for destructive actions.~~ Done, over OpenID Connect: changing
   and removing PINs, deleting profiles and accounts, downloading your data,
   and lifting PIN locks; the PIN lock threshold is a setting.
5. ~~A minimal demo site in plain HTML and JavaScript.~~ Done:
   [examples/demo-site](examples/demo-site).

## Milestone 2: administration

**Overfin's own control panel**, built in and the same for every operator:
the place to run an instance without Jellyfin's dashboard, where Overfin's
users only appear under pseudonyms. It's served on its own listener, never on
the internet (SECURITY.md rule 58), and only answers private addresses.
Browsers keep its login cookie only over HTTPS or on `localhost`, so without
a certificate it's reached through an SSH tunnel (`http://localhost:…`);
with one, straight from the operator's LAN or VPN. Admins sign in through
the identity provider with a fresh login, must be in the admin group, and
stay signed in while active (30 idle minutes end it).

**Settings move to the panel.** The configuration file keeps only what
Overfin needs to start (listen addresses, Jellyfin, the database, the
identity provider, the admin listener; secrets stay in the environment).
Every feature (plugins, limits, adoption, account deletion, privacy fields,
PIN attempts, and later invites, catalog, requests) lives in the database,
is switched on and off from the panel, and applies at once. Everything is
off by default.

1. ~~The admin listener and panel: sign-in, the admin group, and the
   feature settings.~~ Done.
2. Users: every account with its profiles, under real names when a
   connector provides them; disable, re-enable, delete (with their Jellyfin
   users), account expiry, per-account limits, PIN-lock resets, sessions.
3. Invites: created and managed in the panel (expiry, number of uses, a
   template: profiles, kids limits, stream limit, account expiry); redeemed
   on the public site, where they create the account. Optionally, accounts
   only by invite. Invites are access control, so they belong in Overfin
   (unlike social features, which are users' data).
4. Identity provider connectors, optional: Authentik first. Users' real
   names in the panel, and invites that come with an Authentik invitation:
   one link, the person picks their username and password on Authentik's
   own pages, comes back, and their account is set up. Passwords never pass
   through Overfin. Connector tokens are used only by the admin side.
5. Live view: who's watching, method and bitrate, transcode load; stopping a
   stream; history and activity read live from Jellyfin, never stored.
6. Notices, off by default: invite used, account expiring (email, webhook),
   carrying account events only, never viewing activity.

Later, perhaps: letting the configuration file pin a feature setting, for
operators who manage servers as files.

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
- It refers to titles by **external IDs** (TMDB, IMDb, TVDB), not Overfin's
  item IDs: Jellyfin can give an item a new ID (a moved file, a rebuilt
  server), while external IDs name the title itself. They also work across
  Overfin servers, and for titles not in the library. Episodes are the
  show's external ID plus season and episode numbers.
- **Title or copy:** some posts are about the title ("great ending"), some
  about the copy on this server ("audio drifts", "wrong subtitles"); the
  external ID stays the same when the file is replaced. So Overfin would also
  give each media version a **fingerprint** that changes with the file
  (size, duration, container, codecs, resolution), hashed with a key of the
  server's own, so it can't be compared between servers or reveal which
  release someone has. The service stores both; when the fingerprint no
  longer matches, the site shows the post as about an earlier copy. Problem
  reports proper stay with Seerr's issues (the requests plugin).
- It stores no metadata; sites fetch titles and posters from Overfin.
- **Visibility:** before showing anything that names a title, the site looks
  it up through Overfin as the viewer, and hides it on `not-found`. A kids
  profile, or a viewer without access to a library, never learns of a title
  through a friend; Overfin's per-profile limits protect the feed too.
- Overfin's part: `GET /items/lookup?tmdb=…` (or `imdb`, `tvdb`, with
  season and episode), answering with the local item or `not-found`.
  Jellyfin can't search by an external ID's value, so Overfin keeps an index
  from external IDs to items: library-wide, in memory only, no user data,
  rebuilt after a restart. Every answer is the item fetched with the
  viewer's own token. Building the index with the Jellyfin admin key needs a
  narrow exception to SECURITY.md rule 3.

## Open questions

- Step-up checks the login time in the ID token rather than trusting
  `prompt=login`. Confirm each supported identity provider updates it on a
  forced login (Authentik first).
- How long do TMDB's terms allow catalog data to be cached?
- Configuration format details: which settings exist, and their defaults.
