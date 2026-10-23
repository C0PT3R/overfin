# Security

Overfin exists to make a Jellyfin server safe to put behind a public website.
These rules are the point of the project; a change that breaks one is a bug.
[THREAT_MODEL.md](THREAT_MODEL.md) explains which threat each rule answers.

## Hard rules and secure defaults

Overfin must be easy to run, for anyone from a first-time operator to a
network security expert. So the rules come in two kinds:

- **Hard rules** hold on every server, whatever its settings. No setting
  loosens them. Above all, between servers (rules 65 and after): one badly
  run server must never be a way into another.
- **Secure defaults** are where an operator's choice of setup decides, and
  each says what it can be relaxed to. The default is always the strict
  one, and relaxing it only ever affects that operator's own server:
  - **HTTPS** is recommended, not required: Overfin's own (Let's Encrypt),
    a reverse proxy's, or none, for a LAN or a test.
  - **Cookies** (rule 17) are `__Host-` and `Secure` whenever there's
    HTTPS; without it, browsers refuse both, so they go without.
  - **The control panel** (rule 58) is on the site, for admins who confirm
    it's them, with Overfin's own sign-in; it can be kept to private
    addresses, or moved to a private listener of its own, off the site
    entirely; with an identity provider, it has its own private listener.
  - **Jellyfin and Seerr** are best kept off the internet (Deployment,
    below); exposing them is the operator's choice.
  - **Access to Jellyfin itself** (rule 67) is closed: its administrators
    have passwords nobody knows. An admin can open it from the control
    panel, and close it again.

Every rule not named here is a hard rule.

## Rules

### Upstream access
1. Only the gateway talks to Jellyfin, Seerr and TMDB. Every upstream call is a
   typed function on a fixed list. There is no passthrough route, and the plugin
   registrar refuses catch-all patterns.
2. Client input reaches upstream only after checks. In URL paths, only IDs that
   match a strict format may appear (as in `/Items/{id}`). Everything else
   travels as encoded query or body values: parameters come from fixed sets of
   allowed values, and free text (such as a search term) is length-capped.
3. Every call to Jellyfin runs as Overfin's one Jellyfin account: an ordinary
   user of its own, never an administrator, hidden, with every library and no
   limit of Jellyfin's, made with the key Overfin created for itself
   (rule 67; that's provisioning), its token sealed like every other. It holds nothing personal: what each
   profile may see, Overfin decides before anything is shown or played
   (rule 28), and what each profile does is Overfin's to keep (rule 40).
   Each playback runs on a Jellyfin device of its own, so playbacks never
   share a session or a transcode. The Jellyfin admin key is used only by
   provisioning code and the admin API (rule 58), never by the public API's
   user routes. Seerr has no per-user tokens, so Seerr
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
13. Identity comes from Overfin's own sign-in (managed: an email and a code
    sent there, a password, or a one-time link; the default), from an
    OpenID Connect provider through Overfin's login, or from forward-auth
    headers set by a trusted proxy. With Overfin's sign-in or login,
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
16. With Overfin's own sign-in, every request checks the person behind the
    login: someone disabled or removed is out at once, and a login ends
    after 30 days without activity. With an identity provider, sessions are
    revalidated with it at a configured
    interval (with Overfin's login, by refresh token every 10 minutes by
    default; if the provider stays unreachable for an hour, logins fail
    closed). A provider that gives no refresh tokens gets logins that last
    12 hours at most, without extension. Disabling an identity ends its
    sessions and revokes its Jellyfin tokens. The required group is checked
    on every request, so someone taken out of it is out at once.
17. Session cookies are `HttpOnly` and `SameSite=Strict`, and over HTTPS
    they use the `__Host-` prefix and are `Secure`. A secure default: a
    server run without HTTPS (the operator's choice, for a LAN or a test)
    gets cookies without the prefix and the flag, which browsers refuse
    there. A cookie set without HTTPS is never read over HTTPS, nor the
    other way round, and a site whose address is `https://` always gets
    HTTPS cookies.
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
    deletion, exporting the whole account, PIN reset, changing one's
    password) require a fresh login: the password or an emailed code again
    with Overfin's own sign-in, a new login at the identity provider
    otherwise. Frequent sensitive actions (switching to a
    PIN-protected profile, changing a kids profile's rating ceiling or turning
    it into a standard profile) require the PIN. A standard profile exporting
    only its own data needs its own PIN, if it has one: the people using
    other profiles don't know the account's login. Kids profiles can't export
    themselves; their data is in the owner's export. No session ever falls
    back to a PIN-protected owner profile: when the owner has a PIN, a new
    session, or one whose profile was deleted, has no profile until one is
    chosen (`profile-required`).
22. Each wrong PIN adds a growing delay. After a set number of wrong PINs
    (operator setting, default 5), PIN entry on that profile is blocked until
    the account owner confirms a fresh login (rule 21).
    The threshold is `limits.pin_attempts` (3 to 10, default 5), and the
    wait between attempts never exceeds a minute. On an instance without
    step-up, where the owner can't confirm a login, the block lasts 15
    minutes.

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
    the operator's chosen ratings country. The ratings are Overfin's own, from
    TMDB, never the media server's; a title's every certification there must
    be within the ceiling. A missing, unknown, unrated or expired rating means
    the request is refused. Everywhere in the catalog (search, discover, lists,
    title details, images, playback), titles above the ceiling are hidden.
    The same holds for the libraries a profile gets and the tags hidden from
    it, for any profile: Overfin applies them, not Jellyfin, and what it
    can't place in a library is hidden from a profile limited to some.
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
    busy, it plays a version that needs no transcoding if there is one, and
    refuses otherwise.
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
    profile made each request, and short-lived playback state; with the admin
    side, also invites, account expiry and limits, and labels the operator
    sets; with the watchlist, each profile's list of titles (private to it,
    deleted with it); with Overfin's own sign-in, each person's email and,
    if they set one, a hash of their password (PBKDF2-SHA256, 600,000
    rounds), deleted with their account; and what each profile did with each title (where it
    stopped, whether it watched it, how many times and when last, its
    favorites), brought over from Jellyfin once with the profile's own
    token, then kept by Overfin. That belongs to its profile: only that
    profile sees it (and, for a kids profile, its account's owner, rule
    50), never the admins, whom neither the panel nor any admin route
    shows it; it's in the data download (rule 52) and deleted with the
    profile. The admin side reads activity from Jellyfin live, for the page
    being viewed, and never stores it: at most it keeps it in memory, never on
    disk, for a few minutes and within a size limit, so a restart forgets
    it. Names and emails from the identity provider are shown live through
    its connector, the same way, never stored; only the name is handed on,
    to the account's Seerr user (rule 47). The one exception to "never the
    admins": apps the operator gives a key to (rule 58) read totals per
    film and series (plays to the end, viewers counted by account,
    favorites counted by profile, the day it was last played), never
    anything per person. On a small server, a total can still point to
    someone; the operator decides which apps get them.
41. No analytics, no telemetry, no calls home. Metrics are aggregate counts
    with no user labels.
42. Client IPs are used in memory (rate limiting) and never stored by Overfin.
43. Overfin never sends the browser to a third party: images, including TMDB
    posters, are served by the gateway.
44. A public API response never reveals other users' data or activity. Only
    the admin API (rule 58) shows users, to the operator's admins.
45. Deleting an account deletes everything Overfin holds about it. Its Jellyfin
    and Seerr users are deleted or disabled, as the operator configures
    (`accounts.on_delete`), and the site tells the user which.
46. Overfin offers no hooks for extra data collection. Operators who want more
    collect it with their own tools. Social features are out of Overfin's
    scope: it stores no relations between users, comments, likes, messages
    or anything of the kind. Operators who want them build them in their own
    backend, logging users in with the same identity provider.
47. Jellyfin users created by Overfin get opaque names (e.g. `of-7f3k2q`),
    never real names or emails. Seerr users get the account's name as the
    control panel shows it (the identity provider's, else the operator's
    label, else the owner profile's), so Seerr's own pages say who asked
    for what; never an email (theirs is a placeholder on `.invalid`), and
    no one can sign in to Seerr as them. Seerr therefore holds names: keep
    it private, reachable by Overfin and the operator only.
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
    scripts, icons); overfin-web does exactly that.
54. Logs identify users only by a keyed hash (HMAC) of their ID and never
    contain query strings.
55. Privacy-sensitive fields are off by default, and operators turn on each one
    they want: photo capture time, location and camera data, item tags, and
    version names (derived from file names).
    Embedded metadata is still stripped from image files (rule 10).
    Security-sensitive data (file paths, server internals, permission flags) is
    never exposed, whatever the configuration. The one exception is the
    control panel's Identification (rule 58): admins see a film's or a
    series' folder and file names (never the whole path), to tell a title
    Jellyfin matched wrong. The index keeps only those names.
56. Counts (`totalCount`) only include what the active profile may see, so
    hidden items never show up as a number.
57. What's trending, popular or upcoming, titles' details and their
    certifications are asked of TMDB by the server, with the Overfin
    project's own key (or of Seerr, which asks TMDB, when TMDB itself isn't
    there), and their posters of TMDB's image server. Nothing about who's
    asking is sent; browsers never contact Seerr or TMDB (rule 43). The
    project's key is public, as Jellyfin's and Seerr's are, and grants reading
    only; Seerr's API key is kept sealed, like every token. TMDB's data is kept
    as its terms allow: five months at most without being fetched again, and
    deleted entirely when its only source (Seerr) is disconnected.

### Administration
58. Overfin's administration, its built-in control panel, is apart from the
    public API: no admin route can be registered there, and the panel has
    its own routes, guards and sessions. Admins confirm it's them every
    time they sign in to it; a session ends after 30 minutes without
    activity, and after 12 hours at most. Where it lives is a secure
    default:
    - With Overfin's own sign-in, it's at `/admin/` on the site, for people
      marked admin, who confirmed a fresh login (rule 21) in the last five
      minutes; their panel session ends as soon as they sign out of the
      site or stop being admins. `admin.private` keeps it to private
      addresses, with the guard below. With `admin.listen` instead, it's
      only on a listener of its own, with the guard below, which also
      serves the sign-in, so admins sign in there, and the site has no
      panel at all; so does the setup page. In the panel, admins can't take
      the admin mark from themselves or remove themselves.
    - With an identity provider, it's on its own listener, off by default,
      which answers only clients with private addresses (loopback, private
      ranges, unique-local IPv6) and refuses any request a proxy relays for
      a public address, so routing it through a public reverse proxy fails
      closed. How admins reach it (LAN, VPN, SSH tunnel) is the operator's
      choice. Admins sign in there with a fresh login every time, and must
      belong to the configured admin group.

    Until Overfin is set up (Jellyfin, and with Overfin's own sign-in a
    first admin), it serves only the setup page, on the panel's listener
    with its guards, and with Overfin's own sign-in on the site's address
    too: it saves nothing without the one-time code from Overfin's log,
    stops answering after 10 wrong codes until a restart, and checks
    everything before saving anything; Jellyfin's key is kept sealed, like
    every token. Other apps
    (Stewarr…) read what Overfin shares with them on the same listener,
    with the same guards, under /apps/: each with a key an admin made for
    it in the panel, for one purpose ("watch-stats"), shown once, kept only
    as a hash, revocable.

### Code supply
59. No code copied or ported from other projects. Dependencies are few, chosen
    on purpose, and pinned.

### Migration
60. Bringing history over from another Jellyfin server (migration) is off
    by default; the operator names that server to turn it on. It's a copy:
    Overfin signs in there with the person's own username and password,
    reads what they watched, where they stopped and their favorites, keeps
    a copy, and signs out. It changes nothing on that server. The password
    is used for that one sign-in and never stored or logged, and neither is
    the token it gives, which is for that server only, never Overfin's own;
    that's why administrator accounts may copy their history too. Failed
    attempts are limited per user, and wrong usernames and wrong passwords
    get the same answer. An account on the other server can be copied into
    only one Overfin account. Nothing is guessed: no matching by name or
    email, only the username and password.

### Resource use
61. Memory-heavy work, such as decoding images, runs with bounded
    concurrency and bounded caches, so a burst of requests can't exhaust the
    server's memory.

### Features
62. Every feature is built in; nothing is added or loaded while Overfin
    runs, and there's no way to plug code of one's own into it. Features
    written on the internal feature contract (the watchlist, invitations,
    migration) go through the same registrar as the rest (no catch-alls, a
    login required), act only as the calling profile (or, on a visitor's
    route, can only have Overfin set the visitor's account up), and never
    get the media server's admin key. Each talks only to Overfin's core,
    never to another feature, the media server or the core's own data: it
    has a database of its own, holding only its tables, and what features
    share is a core service. The core tells them of deleted profiles and
    accounts, and they delete what they hold about them.

### Invitations
63. Invitation links carry 128 random bits; only their SHA-256 is kept, and
    a link is shown to the admin once. Opening one only keeps it in an
    HttpOnly cookie for a day and leads on to the site; an account is set
    up with it only after a login, under the account-creation lock, so a
    single-use invitation is used once. Uses stay counted when the account
    is deleted. With an identity provider, an invitation decides what an
    account gets, never who may log in: the provider and the required group
    still do. With Overfin's own sign-in, an invitation is what lets a new
    email in (rule 66); everyone else is someone an admin added.

### The site
64. The site Overfin serves (overfin-web, or the operator's folder) goes to
    logged-in, allowed visitors only, the same check as the gate. Only GET
    and HEAD; never a directory listing, never a name starting with a dot,
    never anything outside the folder (it's opened with `os.Root`, which
    symbolic links can't escape). Its files get their own
    Content-Security-Policy (strict by default: everything from the site
    itself), while the API keeps its own, stricter one. Other sites may
    link to its pages, which change nothing; the API still refuses them.

### Between servers
65. Servers never trust each other by default, and nothing here is a secure
    default: these hold whatever either operator chose for their own
    server. When servers share with each other (federation, planned), each
    peer is untrusted, proves who it is with keys exchanged when the two
    were paired (never with a web certificate alone, nor a name or an
    address), and gets only what was shared with it, under the sharing
    server's rules. A server never acts on another's behalf beyond that,
    and never hands one peer's data to another. A weak or compromised
    server can at worst lose what was shared with it; it can never reach
    into the servers that shared it.

### Sign-in by email
66. With Overfin's own sign-in, whether an email has an account here is
    never told: asking for a code gets the same answer and counts against
    the same limit either way, codes are sent in the background, so how
    long sending takes tells nothing, and a wrong password gets the same
    answer as an unknown email. Codes go to an email without an account only for a visitor
    bringing an invitation (rule 63). A code is six digits, works once, for
    15 minutes, only in the browser that asked for it, and five wrong tries
    end it; five codes an hour per email at most. The link in the same
    email works once, in any browser; its token travels in the address's
    fragment, so it never reaches a server's logs, and is used only on a
    click, so mail scanners opening links use nothing. A one-time link an
    admin makes for someone works the same way, for a week. Only keyed
    hashes of codes, links and the browsers they're bound to are kept.
    Passwords are 10 to 256 characters; checking one takes the same time
    whether or not the email has a password, and password tries are
    limited per email. A new password ends the person's other logins.

### Overfin's Jellyfin
67. Overfin's Jellyfin is dedicated to it, and Overfin never changes
    anything on any other Jellyfin. It takes a Jellyfin over only when it's
    fresh (its setup never run: Overfin runs it), when it answers to a key
    Overfin created, or when the operator hands it over: they give an admin
    login, see what that server holds, and confirm it's dedicated to
    Overfin; that login is used once to create Overfin's key, then signed
    out, and never stored. Overfin uses only keys it created itself, kept
    sealed like every token, and revokes the keys it made on earlier
    takeovers. Access to its Jellyfin is closed: every administrator there
    has a password nobody knows, from the takeover on (a server handed
    over is closed at once, once Overfin's key is saved). An admin can
    open access from the control panel, which gives Overfin's own
    administrator account a new password, shown once with a warning that
    changes made in Jellyfin may break Overfin and be undone by it, and
    close it again, which gives every administrator a new password nobody
    knows, ending every sign-in there, and changes nothing else (other
    users stay, and other apps' keys, such as Seerr's, are never touched).
    The settings Overfin needs are its own: it sets them, and sets them
    back on every start. Only when the operator asks (setup wizard or
    control panel), Overfin installs Intro Skipper on its Jellyfin: it adds
    that project's plugin repository, checks that the pinned version's
    checksum is the one Overfin knows, installs it and restarts Jellyfin
    (Jellyfin's own plugin updates stay off, so that version stays);
    removing it from the panel takes the plugin and its repository off
    again. Overfin installs no other plugin.

## Deployment

Overfin needs a Jellyfin of its own (rule 67), on the same media as any
other the operator runs; their other servers are never touched. Until
Overfin has set a fresh Jellyfin up, its setup is open to whoever reaches it
first: keep it reachable only by Overfin until then. Keeping Overfin's
Jellyfin off the internet is recommended, not required: ideally its port is
reachable only from Overfin's host and the operator's admin network.
Overfin's rules protect only what goes through Overfin; whatever reaches
Jellyfin directly is Jellyfin's own business. With an identity provider, Overfin's admin
listener must not be reachable from the internet: publish it only on the LAN or a VPN, never
through the reverse proxy that serves the site (rule 58). Overfin never offers
a route to Jellyfin's dashboard; when access is open (rule 67), operators
reach it privately (LAN, VPN, SSH tunnel).

HTTPS is recommended for any server reachable from the internet: Overfin
gets its own certificate with `https.domain` (or `OVERFIN_DOMAIN`), or a
reverse proxy provides it. Behind a proxy, set `site.url` to the site's
`https://` address, so cookies are HTTPS ones whatever the proxy says, and
`identity.trusted_proxies`, so the client address is the proxy's to give.
Without HTTPS, everything sent, passwords and codes included, travels in
clear: keep that to a LAN or a test.

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

## Reporting a vulnerability

Please report it privately, through this repository's **Security** tab
(*Report a vulnerability*), not in a public issue. You'll get an answer, and
credit if you'd like it, once it's fixed.
