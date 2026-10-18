# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.

## Rules

### Upstream access
1. Only the gateway talks to Jellyfin and Seerr. Every upstream call is a
   typed function on a fixed list. There is no passthrough route, and the
   plugin registrar refuses catch-all patterns.
2. No upstream URL is built from client input. IDs are checked against a strict
   format; other parameters come from fixed sets of allowed values.
3. User actions run with that user's own Jellyfin token. Admin keys are used
   only by provisioning code.
4. Upstream tokens never reach the browser, including inside stream URLs and
   HLS playlists.

### Identity and sessions
5. Identity comes from a configured identity plugin (forward-auth headers from a
   trusted proxy, or OIDC). Forward-auth headers are accepted only from the
   configured proxy address.
6. Sessions use httpOnly, Secure, SameSite cookies. State-changing requests are
   protected against CSRF.
7. Upstream tokens are encrypted at rest, with the key kept outside the database.

### Web hardening
8. Strict response headers: CSP, frame-ancestors none, nosniff, no referrer.
9. Rate limits on login and streaming routes.
10. Upstream errors are mapped to generic responses; internal details stay in
    the server log.
11. Logs never contain tokens, cookies or passwords.

### Privacy
12. Overfin stores only what it needs to run: the account-to-Jellyfin mapping,
    encrypted tokens, sessions, profiles and short-lived playback state. Watch
    history stays in Jellyfin, which already keeps it.
13. No analytics, no telemetry, no calls home. Metrics are aggregate counts
    with no user labels.
14. Client IPs are used in memory (rate limiting) and never stored by Overfin.
15. Overfin never sends the browser to a third party: images, including TMDB
    posters, are served by the gateway.
16. An API response never reveals other users' data or activity.
17. Deleting an account deletes everything Overfin holds about it.
18. Overfin offers no hooks for extra data collection. Operators who want more
    collect it with their own tools.
19. Jellyfin and Seerr users created by Overfin get opaque names (e.g.
    `of-7f3k2q`), never real names or emails. Only Overfin links them to an
    identity.
20. Search terms and other personal input travel in request bodies, never in
    URLs, so they stay out of access logs and browser history.
21. Users never learn who else requested a title, only that it was requested.
22. Profiles are private from each other by default; kids profiles are visible
    to their account owner. Operators can change this.
23. Users can list and end their own sessions and devices, and only their own.
24. Users can download everything Overfin holds about them.
25. Sites built for Overfin should load nothing from third parties (fonts,
    scripts, icons); the demo site does exactly that.
26. Logs identify users only by a hashed ID and never contain query strings.

### Code supply
27. No code copied or ported from other projects. Dependencies are few, chosen
    on purpose, and pinned.

## Reporting a vulnerability

Please don't open a public issue. Use GitHub's private vulnerability reporting
on this repository instead.
