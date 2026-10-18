# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each rule answers.

## Rules

### Upstream access
1. Only the gateway talks to Jellyfin and Seerr. Every upstream call is a
   typed function on a fixed list. There is no passthrough route, and the
   plugin registrar refuses catch-all patterns.
2. No upstream URL path is built from client input. Client values reach
   upstream only as encoded query or body values, after checks: IDs match a
   strict format, other parameters come from fixed sets of allowed values, and
   free text (such as a search term) is length-capped.
3. User actions run with that user's own Jellyfin token. Admin keys are used
   only by provisioning code.
4. Upstream tokens never reach the browser, including inside stream URLs and
   HLS playlists.

### Upstream data
Jellyfin and Seerr are trusted software serving untrusted data: metadata comes
from the internet, and media files can be written by anyone.

5. No URL found in upstream data is ever fetched. Remote and `.strm` media
   sources are not supported.
6. Overfin never follows redirects from Jellyfin or Seerr. Upstream responses
   have size limits and timeouts.
7. IDs that come from upstream are validated exactly like client input.
8. HLS playlists are parsed, not searched and replaced. Only entries that match
   Jellyfin's known segment URLs are kept.
9. Metadata text is served as plain text. Markup, control characters and
   bidirectional overrides are removed, and lengths are capped.
10. Images are decoded and re-encoded (JPEG, or PNG when transparency is
    needed), which strips embedded metadata. Their dimensions are checked
    before decoding, and oversized images are refused. SVG, HTML and anything
    that fails to decode are never served.
11. Subtitles are served only as sanitized WebVTT. Fonts and other files
    attached to media are never served.
12. From upstream responses, only the headers streaming needs are passed on:
    a checked `Content-Type`, `Content-Length`, `Content-Range` and
    `Accept-Ranges`. Never `Set-Cookie` or `Location`.

### Identity and sessions
13. Identity comes from a configured identity plugin (forward-auth headers from
    a trusted proxy, or OIDC). Forward-auth headers are accepted only from the
    configured proxy address, and the proxy must strip identity headers sent
    by clients. A request to a non-anonymous route that carries no identity is
    refused.
14. The client IP is taken only from the trusted proxy (PROXY protocol or a
    forwarded header the proxy sets). Forwarded headers from anyone else are
    ignored.
15. Only identities matching the configured group or claim are provisioned,
    and provisioning is rate-limited.
16. Sessions are revalidated with the identity provider at a configured
    interval. Disabling an identity ends its sessions and revokes its Jellyfin
    tokens.
17. Session cookies use the `__Host-` prefix and are `HttpOnly`, `Secure` and
    `SameSite`.
18. Only same-origin requests are accepted (`Sec-Fetch-Site: same-origin`, or
    `none` for plain navigation); cross-site and same-site requests are
    rejected, so sibling subdomains can't act for the user. State-changing
    requests must be JSON. There is no CORS unless the operator lists allowed
    origins, and those use token auth, not cookies.
19. Upstream tokens are encrypted at rest, with the key kept outside the
    database. PINs are hashed with that key, so a leaked database alone can't be
    used to guess them.
20. Rare, destructive actions (profile transfer, account deletion, data export,
    PIN reset) require a fresh login through the identity provider. Frequent
    sensitive actions (switching to a PIN-protected profile, changing a kids
    profile's rating ceiling) require the PIN.
21. Each wrong PIN adds a growing delay. After a set number of wrong PINs
    (operator setting, default 5), PIN entry on that profile is blocked until
    the account owner logs in again through the identity provider.

### Authorization
22. Every Overfin resource ID (profiles, playback sessions, stream tickets,
    transfer codes) is random, at least 128 bits, and every lookup checks that
    it belongs to the current account.
23. Anything the caller may not see returns 404, never 403.
24. The client never chooses which Jellyfin user acts for it; Overfin derives
    that from the session.
25. An image is served only after checking that the current user may see the
    item it belongs to, also when it comes from Overfin's cache.
26. Kids profiles can request only titles rated at or below their ceiling, in
    the operator's chosen ratings country. A missing or unreadable rating means
    the request is refused. Their search results hide titles above the
    ceiling.
27. Stream tickets are short-lived and bound to the session that created them.
    Transfer codes are long, single-use and expire within minutes.

### Limits
28. Streams and transcodes are limited per account and in total. Images come in
    a fixed set of sizes. Login, search, requests and streaming are
    rate-limited.
29. When an account reaches its stream limit, its oldest stream stops and the
    stopped device is told why (operators can choose to refuse the new stream
    instead). Overfin never stops another user's stream: when the server is
    busy, it tries direct play or lower quality before refusing.

### Web hardening
30. Strict response headers: CSP, frame-ancestors none, nosniff, no referrer.
31. Errors use RFC 9457 problem details with a machine-readable `type`.
    Upstream errors are mapped to generic responses; internal details stay in
    the server log.
32. Logs never contain tokens, cookies or passwords.

### Privacy
33. Overfin stores only what it needs to run: the account-to-Jellyfin mapping,
    encrypted tokens, sessions, profiles and short-lived playback state. Watch
    history stays in Jellyfin, which already keeps it.
34. No analytics, no telemetry, no calls home. Metrics are aggregate counts
    with no user labels.
35. Client IPs are used in memory (rate limiting) and never stored by Overfin.
36. Overfin never sends the browser to a third party: images, including TMDB
    posters, are served by the gateway.
37. An API response never reveals other users' data or activity.
38. Deleting an account deletes everything Overfin holds about it. Its Jellyfin
    and Seerr users are deleted or disabled, as the operator configures, and
    the site tells the user which.
39. Overfin offers no hooks for extra data collection. Operators who want more
    collect it with their own tools.
40. Jellyfin and Seerr users created by Overfin get opaque names (e.g.
    `of-7f3k2q`), never real names or emails. Only Overfin links them to an
    identity.
41. Search terms and other personal input travel in request bodies, never in
    URLs, so they stay out of access logs and browser history.
42. Users never learn who else requested a title, only that it was requested.
43. Profiles are private from each other by default; kids profiles are visible
    to their account owner. Operators can change this.
44. Users can list and end their own sessions and devices, and only their own.
45. Users can download everything Overfin holds about them, except secrets:
    tokens, PIN hashes and keys are never exported.
46. Sites built for Overfin should load nothing from third parties (fonts,
    scripts, icons); the demo site does exactly that.
47. Logs identify users only by a keyed hash (HMAC) of their ID and never
    contain query strings.

### Code supply
48. No code copied or ported from other projects. Dependencies are few, chosen
    on purpose, and pinned.

## Deployment

Jellyfin must not be reachable from the internet. Ideally its port is reachable
only from Overfin's host and the operator's admin network. Overfin never offers
a route to Jellyfin's dashboard; operators reach it privately (LAN, VPN, SSH
tunnel).

Every route to Overfin must pass through the proxy's authentication, and the
proxy must strip client-sent identity and forwarded headers before adding its
own (rules 13 and 14).

## Reporting a vulnerability

Please don't open a public issue. Use GitHub's private vulnerability reporting
on this repository instead.
