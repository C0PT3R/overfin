# Threat model

Overfin sits between the internet and a Jellyfin server (plus an optional
Seerr). It protects Jellyfin from strangers, and users from strangers and from
each other. Numbers in brackets refer to the rules in
[SECURITY.md](SECURITY.md).

## Trust boundaries

```
internet ──▶ reverse proxy ──▶ Overfin ──▶ Jellyfin / Seerr
 untrusted     operator's         the        trusted code,
               (TLS, identity)    boundary   untrusted data
```

| Party | Trusted? |
|---|---|
| Visitors, logged in or not | No |
| The website built on Overfin | Partly: it may have bugs, so Overfin limits what a broken site can do |
| The reverse proxy and identity provider | Yes, for the identity and client IP they assert |
| Other sites on the same domain (sibling subdomains) | No |
| Jellyfin and Seerr software | Yes |
| Data from Jellyfin and Seerr (metadata, files, subtitles, images) | No |
| The operator | Yes, by design |

## Attackers

| Attacker | Goal | Main defenses |
|---|---|---|
| Stranger on the internet | Reach Jellyfin, exploit it, get an account | Small fixed API [1], identity before any route [13], provisioning only for allowed identities [15], rate limits [28] |
| Logged-in, malicious user | Other users' data, more access, exhausting the server | Ownership checks [22–25], limits [28–29] |
| Script injected into the website (XSS) | Act as the user, take over the account | No tokens in reach [4, 17], step-up for lasting damage [20] |
| A compromised sibling subdomain | Make requests in the user's name | Same-origin requests only [18], `__Host-` cookies [17] |
| Attacker-controlled media or metadata | Run code in the browser, reach the internal network, exhaust memory | Untrusted-data rules [5–12] |
| Network observer | See who watches what | TLS, nothing personal in URLs [41] |
| Thief of a database or backup | Tokens, identities, PINs | Encryption and keyed PIN hashes [19], pseudonyms [40], minimal data [33] |

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
  the script, so PINs only guard frequent, recoverable actions [20].
- **Overfin's own responses never become the XSS.** Images are re-encoded and
  never SVG or HTML [10], text is plain [9], and every response carries a
  restrictive CSP and `nosniff` [30].

## Case 2: a malicious logged-in user

The user controls their browser and can call the API directly with any input.
Nothing is enforced by the UI.

- **Other people's things:** Overfin IDs are random and checked against the
  current account [22]. Library items are filtered by Jellyfin itself, because
  calls use the user's own token [3]. Jellyfin's image endpoints don't check
  permissions, so Overfin checks every image itself, cached or not [25].
  Hidden things return 404, so their existence doesn't leak [23].
- **Escaping a kids profile:** PINs are checked on the server, wrong guesses
  slow down and then require the owner to log in again, and the client never
  picks which Jellyfin user acts [20, 21, 24]. Kids can only request titles
  within their rating ceiling, and unknown ratings are refused [26].
- **Exhausting resources:** stream, transcode and rate limits, fixed image
  sizes [28]. Limits fail gently and never cut off another user [29]. Faking
  `X-Forwarded-For` doesn't dodge rate limits, because the client IP comes only
  from the trusted proxy [14].
- **Injecting upstream:** strict IDs, allowed values and length caps [2].
- **Brute force and link sharing:** long single-use transfer codes,
  short-lived session-bound stream tickets [27]. Tickets are not bound to IP
  addresses: phones change networks mid-stream, and storing IPs would break
  [35].
- **Spying through upstream data:** Overfin's data model has no fields for
  other users' activity or for who requested a title [37, 42].

## Case 3: untrusted upstream data

Jellyfin is trusted, but much of what it serves isn't. Metadata comes from
internet providers, subtitles from subtitle sites, and media files (with their
embedded titles, chapters, subtitles and attachments) from anywhere.

- **Server-side request forgery:** a `.strm` file or remote media source can
  point at any URL, including the operator's internal network. Overfin never
  fetches URLs from data and never follows upstream redirects [5, 6].
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
  any the client sent; a protected route with no identity is refused [13].
- **Open sign-up at the identity provider:** only identities in the configured
  group or claim get Jellyfin users, at a limited rate [15].
- **Offboarding:** sessions are revalidated, and disabling an identity ends
  them and revokes its Jellyfin tokens [16].

## Known limitations

- **Stream tickets appear in the reverse proxy's access logs,** because media
  players request them by URL. Their short lifetime keeps this harmless [27],
  but operators should know.
- **Kids profiles are only as strong as the PINs on the adult profiles.** PINs
  are optional; without them, a child can switch to an adult profile.
- **Deleting an account doesn't necessarily delete its Jellyfin history.** The
  operator chooses whether its Jellyfin and Seerr users are deleted or only
  disabled [38].

## Out of scope

- **A compromised Jellyfin or Seerr.** Overfin can't protect the media server
  itself, but rules 5–12 keep a compromised upstream from automatically
  compromising Overfin and its users.
- **A compromised user device.** No web application can help there.
- **The operator.** They run the server and can see everything on it. Overfin's
  privacy rules protect users from strangers and from each other, not from the
  server's owner.
