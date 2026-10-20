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
   later (now from the migration module's tab). No matching by name or email: only the
   username and password prove an account.
3. ~~Kids profiles (rating ceilings enforced by Jellyfin) and PINs.~~ Done.
4. ~~Step-up for destructive actions.~~ Done, over OpenID Connect: changing
   and removing PINs, deleting profiles and accounts, downloading your data,
   and lifting PIN locks; the PIN lock threshold is a setting.
5. ~~A minimal demo site in plain HTML and JavaScript.~~ Done; it has
   since become overfin-web ([its own repository](https://github.com/C0PT3R/overfin-web)).

## Next: the core and its modules

Overfin is becoming more than a gateway, so it splits in two: a small
**gateway core** that stays the security boundary (the server and its
guards, identity, sessions and step-up, accounts and profiles, access to the
media backend as a profile, the module system), and **modules** grafted onto
it (library, playback, watchlist, invites, catalog, requests, lists…). The
core never imports a module.

Modules are compiled in, the way Caddy's are: the official build includes
the official modules; an operator who wants their own builds Overfin with a
few-line `main.go` that adds it. Nothing is ever loaded while running. The
module API becomes a public, versioned promise. Official modules carry
Overfin's guarantees; a third-party module is its developer's
responsibility, as with any program, and the operator's for running it.

1. ~~The module contract~~ (the `module` package): routes behind the
   core's guards, the calling profile, the backend as that profile only
   (never the admin key), the module's own tables and migrations, problems,
   logging, and a hook for the data export. Since: control panel tabs and
   routes, module settings, account gates, visitor routes, setup offers
   and invitation links.
2. ~~The watchlist moved onto it, as the pilot~~ (`modules/watchlist`,
   built only on the public contract); then library and playback.
2b. ~~Overfin's startup as an importable package
   (`overfin.Main(modules…)`), so an operator's own build is a few
   lines.~~ Done.
3. ~~Invites, the first module written against the contract.~~ Done,
   after migration.
4. ~~A "Writing a module" guide with a small example.~~ Done:
   [MODULES.md](MODULES.md), with an example compiled and tested with
   Overfin.

**Media backends as modules.** The backend becomes an interface written in
terms of what Overfin needs, with each backend declaring what it can do;
Overfin fails closed (a backend that can't enforce a rating ceiling offers
no kids profiles). Jellyfin 12 is extracted as the first implementation, the
Jellyfin 10.11 adapter proves the interface isn't shaped like one backend,
and others (Emby, perhaps Plex) follow on demand. Later, **several backends
at once**: each profile with its own user on each, merged listings (the same
title recognized across servers by its TMDB or IMDb ID), limits counted
across all. Item IDs are ready for it: `item_<id>` means the default
backend, and any other backend's IDs will name it (`item_<backend>_<id>`), so
nothing stored today ever breaks.

**Connectors to sibling tools**, later: for example Stewarr (storage
intelligence for the *arr stack), which could read what Overfin knows
(watchlist counts per title, never who) and tell Overfin what's leaving soon.

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
Every feature (plugins and their settings, limits, account deletion,
privacy fields, PIN attempts, and later invites, catalog, requests) lives in
the database, is switched on and off from the panel, and applies at once.
Everything is off by default.

1. ~~The admin listener and panel: sign-in, the admin group, and the
   feature settings.~~ Done.
2. ~~Users: every account with its profiles; disable, re-enable, delete
   (with their Jellyfin users), account expiry, per-account limits,
   PIN-lock resets, sessions.~~ Done, with labels; real names come with a
   connector.
2c. ~~Account templates~~ Done: what an account may do (streams,
   profiles; later, which modules), followed live, so editing a template
   changes every account on it. A Default exists from the start, so
   accounts can be opened at once; it starts minimal, is edited like any
   other, and only can't be deleted. Operators make their own. Access length stays individual. A template only sets
   how many profiles an account may have: every account starts with its
   owner profile alone, never a kids one, and its owner creates the rest.
2d. ~~Migration as a module~~ Done (`migration`): bringing old accounts and
   their history over, the same on every backend. Claiming an old account
   with its username and password (move or link), and moving linked
   accounts from its own panel tab. How an existing user is checked and
   adopted is the backend's capability (Jellyfin has it); a backend without
   it makes the module say so instead of offering it. A new server, or one
   done migrating, simply doesn't use it. An old account is one person and
   one history: migrating it creates the account with its owner profile
   only (Jellyfin and Emby have no profiles to bring). A backend with
   profiles of its own (Plex's managed users, perhaps) could declare it can
   list them, and each would come over as a profile; until one does,
   migration brings one. Later:
   **personal invites from a list** of usernames and emails (a plain CSV,
   exported from whatever tool the operator used): one single-use invite
   per old account, tied to it, so receiving the email is the proof and no
   old password is needed. Overfin integrates with no user manager.
3. ~~Invites~~ Done (the `invites` module): made in the panel from invite
   templates (an account template, how long access lasts, how long the
   link works, how many uses), each for someone; the link is shown once
   and only its hash kept. Opening it keeps the invitation in a cookie,
   and logging in sets the account up with it. Optionally, accounts only
   by invitation (bringing a previous account still works). Invites are
   access control, so they belong in Overfin (unlike social features,
   which are users' data).
4. Identity provider connectors, optional: Authentik first. Users' real
   names in the panel (done), and invites that come with an Authentik invitation:
   one link, the person picks their username and password on Authentik's
   own pages, comes back, and their account is set up. Passwords never pass
   through Overfin. Connector tokens are used only by the admin side.
5. Live view: who's watching, method and bitrate, transcode load; stopping a
   stream; history and activity read live from Jellyfin, never stored.
6. Notices, off by default: invite used, account expiring (email, webhook),
   carrying account events only, never viewing activity.
7. Email: an SMTP server set in the configuration, shared by everything
   that sends mail (notices, personal invites, step-up codes). Mail names
   the operator's site, never the software underneath. First use, **step-up
   by emailed code**, an operator's choice beside the fresh login: before a
   guarded action (removing or changing a PIN, deleting a profile or the
   account, downloading one's data), the owner gets a 6-digit code at the
   address the identity provider gave at login, used then, never stored.
   Codes last 10 minutes, are single-use, tied to the session and the
   action, kept only as hashes, and limited in attempts like PINs. No
   weaker in practice than the login itself, which the same mailbox can
   reset. Needs the provider's `email` claim, so not in forwardauth mode.

Later, perhaps: letting the configuration file pin a feature setting, for
operators who manage servers as files.

## Milestone 3: simple to run

Running Overfin should take minutes, not an afternoon, without giving up
what makes it worth running: the control panel never on the internet,
Jellyfin never exposed, HTTPS required, passwords only ever at the identity
provider.

1. **Releases, with a ready-made image** (done, waiting for the first
   version tag): signed, multi-architecture, on
   GitHub's registry, holding the core and the official modules (each off
   until switched on). Custom builds stay as easy: a builder image takes a
   list of modules (`--build-arg MODULES=…`), writes the few lines of
   `main.go` and compiles, like `xcaddy`.
2. **The identity provider made easy**: ~~an Authentik blueprint and a
   compose bundle~~ done ([deploy/](deploy)): Overfin, Caddy and Authentik
   in one compose, Authentik set up by a blueprint (login client, a login
   page with username and password together, the admins group, the
   connector's read-only user), every secret generated by `setup.sh`. Next:
   tested recipes for simpler providers (Pocket ID first). Overfin itself
   never handles passwords.
3. **A first-run setup page**, on the private panel address: the Jellyfin
   address and key, the identity provider and the site address, each
   tested before it's saved. It opens only with a one-time code printed in
   the log, so nobody else can claim a fresh install. The configuration
   file shrinks to almost nothing.
4. ~~**Overfin serves the site itself**~~ Done: the core serves the site
   its build includes; the official build bundles overfin-web (the former
   demo site, now
   [its own repository](https://github.com/C0PT3R/overfin-web), an app on
   the public API, which the core never imports). It's served by default,
   to logged-in visitors only, so the reverse proxy only passes everything
   on (`reverse_proxy overfin:8080`). `[site] serve` takes a folder with your
   own client instead (served live, never outside the folder, with your
   CSP, optionally as a single-page app), or `"none"` to serve it
   elsewhere.
5. **`overfin -check`**: tests the configuration, reaches Jellyfin, fetches
   the provider's discovery document, checks the proxy is trusted, and says
   in plain words what's wrong.
6. **The secret key generated on first start**, into its own secrets
   folder, apart from the database it protects; operators who want more
   still supply their own.

## Beyond Jellyfin

Overfin keeps looking for what people miss in Jellyfin's own experience and
offers it, built on what Jellyfin already stores wherever possible, so
Overfin keeps holding as little as it can.

- ~~**Watchlist**, per profile.~~ Done (the `watchlist` plugin): movies,
  whole shows, seasons or single episodes, as the person chooses; a show
  brings its new episodes along. Overfin keeps the list itself, because
  Jellyfin's playlists expand a show into its episodes; titles are always
  read with the profile's token, so kids limits apply. Next: marking or
  dropping what's been watched.
- **Personal lists**, per profile (`/me/lists`): create, rename and delete
  lists, add, remove and reorder titles, kept like the watchlist.
  Distinct from the operator's curated `/lists`.
- **Titles not in the library yet** on the watchlist: they have no Jellyfin
  item, so they come with the TMDB catalog and Seerr requests plugins (Seerr
  keeps watchlists of its own).

## Designed, to implement later

- TMDB catalog plugin: discover, catalog titles, curated lists.
- Seerr requests plugin: requests, quotas, problem reports.
- Profile avatars, profile transfers.
- Trickplay, lyrics, bitrate test.

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
- **Example clients**, beside overfin-web: small sites in common
  frameworks, showing how to build on the API and the SDK.

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
