# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each rule answers.

## Rules

### Upstream access
1. Only the gateway talks to Jellyfin, Seerr and TMDB. Every upstream call is a
   typed function on a fixed list. There is no passthrough route, and the plugin
   registrar refuses catch-all patterns.
2. Client input reaches upstream only after checks. In URL paths, only IDs that
   match a strict format may appear (as in `/Items/{id}`). Everything else
   travels as encoded query or body values: parameters come from fixed sets of
   allowed values, and free text (such as a search term) is length-capped.
3. User actions run with that user's own Jellyfin token. The Jellyfin admin key
   is used only by provisioning code. Seerr has no per-user tokens, so Seerr
   calls use its API key with the acting user named in the request; that user
   is always the account's own Seerr user, derived from the session.
4. Upstream tokens never reach the browser, including inside stream URLs and
   HLS playlists.

### Upstream data
Jellyfin, Seerr and TMDB are trusted services serving untrusted data: metadata
comes from the internet, and media files can be written by anyone.

5. No URL found in upstream data is ever fetched. Remote and `.strm` media
   sources are not supported. The only URLs Overfin takes from Jellyfin are the
   stream URLs Jellyfin generates itself, and only as relative paths matching
   Jellyfin's known streaming routes, never absolute URLs. The only paths
   Overfin takes from TMDB data are image paths, checked against a strict
   format and fetched only from TMDB's fixed image host.
6. Overfin never follows redirects from Jellyfin, Seerr or TMDB. Upstream responses
   have size limits and timeouts.
7. IDs that come from upstream are validated exactly like client input.
8. HLS playlists are parsed, not searched and replaced. Only entries that match
   Jellyfin's known segment URLs are kept.
9. Metadata text is served as plain text. Markup, control characters and
   bidirectional overrides are removed, and lengths are capped.
10. Images are decoded and re-encoded (JPEG, or PNG when transparency is
    needed), which strips embedded metadata. Their dimensions are checked
    before decoding, and oversized images are refused. SVG, HTML and anything
    that fails to decode are never served. The one exception is trickplay
    sheets: Jellyfin generates them itself from video frames, so they are
    checked (real JPEG bytes, size and dimension limits) but not re-encoded.
11. Subtitles are served only as sanitized WebVTT. Fonts and other files
    attached to media are never served.
12. From upstream responses, only the headers streaming needs are passed on: a
    checked `Content-Type`, `Content-Length`, `Content-Range` and
    `Accept-Ranges`. Never `Set-Cookie` or `Location`.

### Identity and sessions
13. Identity comes from Overfin's own OpenID Connect login (recommended), or
    from forward-auth headers set by a trusted proxy. With Overfin's login,
    identity headers are ignored entirely, whoever sends them; a login
    answer only completes in the browser that started it. Forward-auth
    headers are accepted only from the configured proxy address, and the proxy must strip identity headers sent by
    clients. An identity header that appears more than once is not believed
    at all. Outside anonymous mode, a request to any non-public route that
    carries no identity is refused; routes are protected unless explicitly
    registered as public.
14. The client IP is taken only from the trusted proxy (PROXY protocol or a
    forwarded header the proxy sets). Forwarded headers from anyone else are
    ignored.
15. Only identities matching the configured group or claim are provisioned, and
    provisioning is rate-limited.
16. Sessions are revalidated with the identity provider at a configured
    interval (with Overfin's login, by refresh token every 10 minutes by
    default; if the provider stays unreachable for an hour, logins fail
    closed). A provider that gives no refresh tokens gets logins that last
    12 hours at most, without extension. Disabling an identity ends its sessions and revokes its Jellyfin
    tokens.
17. Session cookies use the `__Host-` prefix and are `HttpOnly`, `Secure` and
    `SameSite=Strict`.
18. Only same-origin requests are accepted (`Sec-Fetch-Site: same-origin`, or
    `none` for plain navigation); cross-site and same-site requests are
    rejected, so sibling subdomains can't act for the user. State-changing
    requests must be JSON, except image uploads, which must carry an image
    content type (plain HTML forms can send neither). There is no CORS unless
    the operator lists allowed origins, and those use token auth, not cookies.
19. After a login or step-up, users are sent back only to the same origin or to
    an origin the operator allows, never to an address taken unchecked from the
    request.
20. Upstream tokens are encrypted at rest, with the key kept outside the
    database. PINs are hashed with that key, so a leaked database alone can't
    be used to guess them.
21. Rare, destructive actions (profile transfer, profile deletion, account
    deletion, data export, PIN reset) require a fresh login through the
    identity provider. Frequent sensitive actions (switching to a PIN-protected
    profile, changing a kids profile's rating ceiling or turning it into a
    standard profile) require the PIN.
22. Each wrong PIN adds a growing delay. After a set number of wrong PINs
    (operator setting, default 5), PIN entry on that profile is blocked until
    the account owner logs in again through the identity provider.
    *The threshold is fixed at 5 for now. On an instance without step-up,
    where the owner can't confirm a login, the block lasts 15 minutes.*

### Authorization
23. Every Overfin resource ID (profiles, playback sessions, stream tickets,
    transfer codes) is random, at least 128 bits, and every lookup checks that
    it belongs to the current account.
24. Anything the caller may not see returns 404, never 403. A 403 only ever
    means the caller can see the thing but may not do this to it (owner-only
    actions, or a missing step-up).
25. Account-wide actions are reserved for the owner profile: managing profiles,
    deleting the account, exporting the whole account, and ending other
    devices' sessions.
26. The client never chooses which Jellyfin or Seerr user acts for it; Overfin
    derives that from the session.
27. An image is served only after checking that the active profile may see
    what it belongs to (a library item, a catalog title, or a profile of the
    same account), also when it comes from Overfin's cache.
28. Kids profiles can request only titles rated at or below their ceiling, in
    the operator's chosen ratings country. A missing or unreadable rating means
    the request is refused. Everywhere in the catalog (search, discover, lists,
    title details), titles above the ceiling are hidden.
29. Every entry of a play queue is checked against the active profile, when the
    queue is set and again when it advances.
30. Stream tickets are short-lived and bound to the session that created them.
    Transfer codes are long, single-use and expire within minutes.

### Limits
31. Streams and transcodes are limited per account and in total. Images come in
    a fixed set of sizes. Login, search, requests, streaming, bitrate tests and
    transfer-code attempts are rate-limited.
32. When an account reaches its stream limit, its oldest stream stops and the
    stopped device is told why (operators can choose to refuse the new stream
    instead). Overfin never stops another user's stream: when the server is
    busy, it tries direct play or lower quality before refusing.
33. A transcode is stopped as soon as its playback stops, its ticket expires or
    its progress reports go silent, so abandoned streams never keep the server
    busy.

### Web hardening
34. Strict response headers: CSP, frame-ancestors none, nosniff, no referrer.
35. Errors use RFC 9457 problem details with a machine-readable `type`.
    Upstream errors are mapped to generic responses; internal details stay in
    the server log.
36. Logs never contain tokens, cookies, passwords or PINs.
37. Public endpoints (`/health`, `/info`) never reveal software versions,
    upstream servers or anything about users.
38. Cursors are signed and bound to the query that produced them. A modified
    cursor, or one used with a different query, is refused.
39. Uploaded images (profile avatars) get the same treatment as rule 10, with
    stricter size limits, and are rate-limited. Overfin stores them itself,
    never in Jellyfin, whose image endpoints don't check permissions.

### Privacy
40. Overfin stores only what it needs to run: the account-to-Jellyfin mapping,
    encrypted tokens, sessions, profiles (with their uploaded avatars), which
    profile made each request, and short-lived playback state. Watch history
    stays in Jellyfin, which already keeps it.
41. No analytics, no telemetry, no calls home. Metrics are aggregate counts
    with no user labels.
42. Client IPs are used in memory (rate limiting) and never stored by Overfin.
43. Overfin never sends the browser to a third party: images, including TMDB
    posters, are served by the gateway.
44. An API response never reveals other users' data or activity.
45. Deleting an account deletes everything Overfin holds about it. Its Jellyfin
    and Seerr users are deleted or disabled, as the operator configures, and
    the site tells the user which.
46. Overfin offers no hooks for extra data collection. Operators who want more
    collect it with their own tools.
47. Jellyfin and Seerr users created by Overfin get opaque names (e.g.
    `of-7f3k2q`), never real names or emails. Only Overfin links them to an
    identity.
48. Search terms and other personal input travel in request bodies, never in
    URLs, so they stay out of access logs and browser history.
49. Users never learn who else requested a title, only that it was requested.
50. Profiles are private from each other by default; kids profiles are visible
    to their account owner. Operators can change this.
51. The owner profile can list and end all of the account's sessions; other
    profiles only see and end the sessions currently using them.
52. Users can download what Overfin holds about them, except secrets: tokens,
    PIN hashes and keys are never exported. The owner profile's export covers
    the account; any other profile's export covers only that profile.
53. Sites built for Overfin should load nothing from third parties (fonts,
    scripts, icons); the demo site does exactly that.
54. Logs identify users only by a keyed hash (HMAC) of their ID and never
    contain query strings.
55. Privacy-sensitive fields are off by default, and operators turn on each one
    they want: photo capture time, location and camera data, item tags, and
    version names (derived from file names).
    Embedded metadata is still stripped from image files (rule 10).
    Security-sensitive data (file paths, server internals, permission flags) is
    never exposed, whatever the configuration.
56. Counts (`totalCount`) only include what the active profile may see, so
    hidden items never show up as a number.
57. When the TMDB catalog is enabled, catalog searches and lookups are sent
    from the server to TMDB. The plugin is off by default and its documentation
    says so; browsers still never contact TMDB (rule 43).

### Administration
58. Overfin's own administration, when it exists, is served only on a separate
    listener meant for the operator's private network, never on the public API.
    Until then, operators manage Overfin through its configuration file.

### Code supply
59. No code copied or ported from other projects. Dependencies are few, chosen
    on purpose, and pinned.

### Adoption
60. Bringing an existing Jellyfin account is off by default. When enabled, the
    account's password is used once to prove ownership and is never stored or
    logged; failed attempts are limited per user, and wrong usernames and
    wrong passwords get the same answer. Administrator accounts are never
    adopted, and a Jellyfin account can belong to only one Overfin account.
    Adopted accounts keep their names until the operator pseudonymizes them,
    an exception to rule 47.

### Resource use
61. Memory-heavy work, such as decoding images, runs with bounded
    concurrency and bounded caches, so a burst of requests can't exhaust the
    server's memory.

## Deployment

Jellyfin must not be reachable from the internet. Ideally its port is reachable
only from Overfin's host and the operator's admin network. Overfin never offers
a route to Jellyfin's dashboard; operators reach it privately (LAN, VPN, SSH
tunnel).

With Overfin's own login, the proxy only forwards `/api/*` to Overfin and
can protect the site's pages by asking `/api/v1/gate` first (forward auth to
Overfin). With forward-auth from another provider instead, every route to
Overfin except the public ones (`/api/v1/health`, `/api/v1/info`) must pass
through the proxy's authentication. In both cases, on every route, the proxy
must strip client-sent forwarded headers, and with forward-auth also
identity headers, before adding its own (rules 13 and 14).

## Reporting a vulnerability

Please don't open a public issue. Use GitHub's private vulnerability reporting
on this repository instead.
