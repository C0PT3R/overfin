# Threat model

Overfin sits between the internet and a Jellyfin server (plus optional TMDB and
Seerr plugins). It protects Jellyfin from strangers, and users from strangers and from
each other. Numbers in brackets refer to the rules in
[SECURITY.md](SECURITY.md).

## Trust boundaries

```
internet ──▶ reverse proxy ──▶ Overfin ──▶ Jellyfin / Seerr / TMDB
 untrusted     operator's         the        trusted code,
               (TLS, identity)    boundary   untrusted data
```

| Party | Trusted? |
|---|---|
| Visitors, logged in or not | No |
| The website built on Overfin | Partly: it may have bugs, so Overfin limits what a broken site can do |
| The reverse proxy and identity provider | Yes, for the identity and client IP they assert |
| Other sites on the same domain (sibling subdomains) | No |
| Jellyfin, Seerr and TMDB as services | Yes |
| Data from Jellyfin, Seerr and TMDB (metadata, files, subtitles, images) | No |
| The operator | Yes, by design |

## Attackers

| Attacker | Goal | Main defenses |
|---|---|---|
| Stranger on the internet | Reach Jellyfin, exploit it, get an account | Small fixed API [1], identity before any route [13], provisioning only for allowed identities [15], rate limits [31] |
| Logged-in, malicious user | Other users' data, more access, exhausting the server | Ownership checks [23, 24, 26, 27], owner-only account actions [25], checked queues and signed cursors [29, 38], limits [31, 32] |
| Script injected into the website (XSS) | Act as the user, take over the account, send users to a phishing page | No tokens in reach [4, 17], step-up for lasting damage [21], checked return addresses [19] |
| A compromised sibling subdomain | Make requests in the user's name | Same-origin requests only [18], `__Host-` cookies [17] |
| Attacker-controlled media or metadata | Run code in the browser, reach the internal network, exhaust memory | Untrusted-data rules [5–12] |
| Network observer | See who watches what | TLS, nothing personal in URLs [48] |
| Thief of a database or backup | Tokens, identities, PINs | Encryption and keyed PIN hashes [20], pseudonyms [47], minimal data [40] |

## Case 1: a compromised website

A site built on Overfin has an XSS bug, so an attacker's script runs in a
logged-in user's browser on that site.

- **It can't steal anything durable.** Session cookies are `HttpOnly` and
  `__Host-` scoped [17], and Jellyfin tokens never reach the browser [4].
- **Nothing else can do what it does.** Only same-origin requests are
  accepted, which excludes other sites and sibling subdomains alike, and state
  changes must be JSON [18]. The compromised site is the only way in.
- **It can act as the user, but not do lasting damage.** Rare destructive
  actions need a fresh login on the identity provider's own domain, which the
  script can't read or fake. A PIN typed into the page could be captured by
  the script, so PINs only guard frequent, recoverable actions [21].
- **Overfin's own responses never become the XSS.** Images are re-encoded and
  never SVG or HTML [10], text is plain [9], and every response carries a
  restrictive CSP and `nosniff` [34].
- **It can't use Overfin to send users elsewhere.** After a login or step-up,
  users only return to the same origin or an origin the operator allows, so
  Overfin's domain can't be used as a springboard to a phishing page [19].

## Case 2: a malicious logged-in user

The user controls their browser and can call the API directly with any input.
Nothing is enforced by the UI.

- **Other people's things:** Overfin IDs are random and checked against the
  current account [23]. Library items are filtered by Jellyfin itself, because
  calls use the user's own token [3]. Jellyfin's image endpoints don't check
  permissions, so Overfin checks every image itself, cached or not [27].
  Hidden things return 404, so their existence doesn't leak [24].
- **Acting for the whole account:** account-wide actions (managing profiles,
  deleting the account, exporting all of it, ending other devices' sessions)
  are reserved for the owner profile [25, 51, 52].
- **Escaping a kids profile:** PINs are checked on the server, wrong guesses
  slow down and then require the owner to log in again, and the client never
  picks which Jellyfin user acts [21, 22, 26]. Kids can only request titles
  within their rating ceiling, and unknown ratings are refused [28].
- **Exhausting resources:** stream, transcode and rate limits, fixed image
  sizes [31]. Limits fail gently and never cut off another user [32]. Faking
  `X-Forwarded-For` doesn't dodge rate limits, because the client IP comes only
  from the trusted proxy [14].
- **Injecting upstream:** strict IDs, allowed values and length caps [2].
- **Smuggling through cursors, queues or counts:** cursors are signed and bound
  to their query [38], every queue entry is checked against the profile [29],
  and counts never include hidden items, so they can't reveal them [56].
- **Brute force and link sharing:** long single-use transfer codes,
  short-lived session-bound stream tickets [30]. Tickets are not bound to IP
  addresses: phones change networks mid-stream, and storing IPs would break
  [42].
- **Spying through upstream data:** Overfin's data model has no fields for
  other users' activity or for who requested a title [44, 49].

## Case 3: untrusted upstream data

Jellyfin, Seerr and TMDB are trusted, but much of what they serve isn't.
Metadata comes from internet providers and TMDB's contributors, subtitles from
subtitle sites, and media files (with their embedded titles, chapters,
subtitles and attachments) from anywhere.

- **Server-side request forgery:** a `.strm` file or remote media source can
  point at any URL, including the operator's internal network. Overfin never
  fetches URLs from data and never follows upstream redirects [5, 6]. The only
  exceptions are Jellyfin's own stream paths and TMDB image paths, both
  format-checked and fetched only from their fixed host [5].
- **Redirecting the browser:** playlists are parsed and filtered [8], and only
  a few upstream headers are passed on [12].
- **Script or markup in metadata:** plain text only [9].
- **Malicious images and subtitles:** images are re-encoded [10], subtitles are
  sanitized WebVTT [11].
- **Oversized or malformed data:** size limits and timeouts, upstream IDs
  validated like client input, and image dimensions checked before decoding,
  so a small file claiming to be enormous can't exhaust memory [6, 7, 10].

## Identity edge cases

- **Spoofed identity headers:** accepted only from the proxy, which must strip
  any the client sent. If a client-sent header slips through next to the
  proxy's, the duplicate makes the identity invalid rather than letting the
  first one win. A protected route with no identity is refused, and every
  route is protected unless explicitly public [13].
- **Open sign-up at the identity provider:** only identities in the configured
  group or claim get Jellyfin users, at a limited rate [15].
- **Offboarding:** sessions are revalidated, and disabling an identity ends
  them and revokes its Jellyfin tokens [16].
- **Taking over someone else's history:** adopting an existing Jellyfin
  account requires its password, attempts are limited, and an account can be
  adopted only once. Administrator accounts are never adopted, so Overfin
  never holds an administrator's token [60].

## Known limitations

- **Stream tickets appear in the reverse proxy's access logs,** because media
  players request them by URL. Their short lifetime keeps this harmless [30],
  but operators should know.
- **Kids profiles are only as strong as the PINs on the adult profiles.** PINs
  are optional; without them, a child can switch to an adult profile.
- **Deleting an account doesn't necessarily delete its Jellyfin history.** The
  operator chooses whether its Jellyfin and Seerr users are deleted or only
  disabled [45].
- **Catalog searches reach TMDB.** With the TMDB catalog enabled, what users
  search for in the catalog is sent from the server to TMDB. Browsers never
  contact TMDB, but the operator's server does [43, 57].
- **Step-up trusts the identity provider's login time.** Overfin asks for a
  new login over OpenID Connect and accepts it only if the signed ID token is
  for the same user and says the login happened after the step-up began. A
  provider that reports a wrong login time would defeat that check. Without
  step-up configured, the actions that need it are refused, and PIN locks end
  after 15 minutes instead of waiting for the owner [21, 22].

## Out of scope

- **A compromised Jellyfin, Seerr or TMDB.** Overfin can't protect those
  services themselves, but rules 5–12 keep a compromised upstream from automatically
  compromising Overfin and its users.
- **A compromised user device.** No web application can help there.
- **The operator.** They run the server and can see everything on it. Overfin's
  privacy rules protect users from strangers and from each other, not from the
  server's owner.
