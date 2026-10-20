# overfin-web

Overfin's own web client, served by Overfin by default: plain HTML, CSS and
JavaScript on the [JavaScript SDK](../sdk/js), with no framework and no build
step. `app.js` is only screens; the SDK does the rest. `overfin.js` here is
a copy of the SDK, kept identical by a test.

- **Account setup**: start fresh, arrive with an invitation, or bring a
  previous account over.
- **Who's watching?**: profiles, PINs, adding a profile (kids profiles with
  a rating ceiling).
- **Profiles and devices**: renaming, kids limits, setting, changing and
  removing PINs, deleting profiles, signed-in devices; the page asks for a
  PIN or a fresh login when the server wants one.
- **Account**: downloading your data, deleting your account.
- **Home**: continue watching, next up, latest per library.
- **Libraries**: sorting, a repeatable shuffle, infinite scroll with cursors.
- **Item pages**: series, seasons, episodes, cast, the watchlist.
- **Player**: Overfin picks direct play, repackaging or transcoding from what
  the browser declares; audio and subtitle choice, "Skip intro", progress
  reporting, resume, and a clear message when another device takes over.

Every screen lives in the address (`#/item/…`, `#/play/…`), so a reload
comes back to it; playback resumes from the position the player last
reported. Text from the server is only ever set with `textContent`.

## How it's served

The files are compiled into Overfin (`web.go`) and served at the site's
root, to logged-in visitors only, with a strict Content-Security-Policy:
everything comes from the site itself. It needs the `library` and
`playback` plugins.

To serve your own client instead, point `[site] serve` at its folder in
Overfin's configuration; to serve the site some other way, set it to
`"none"`.

## Third-party code

`vendor/hls-1.7.3.min.js` is [hls.js](https://github.com/video-dev/hls.js)
1.7.3, unmodified, under the Apache License 2.0 (`vendor/hls.js-LICENSE`).
It plays HLS streams in browsers without native support. It's served from
the site itself, so browsers never contact a third party.
