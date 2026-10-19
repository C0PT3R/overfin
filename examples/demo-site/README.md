# Overfin demo site

A small but complete website on Overfin's [JavaScript SDK](../../sdk/js), in
plain HTML, CSS and JavaScript: no framework, no build step. It's meant to be
read: every screen shows how a client uses one part of the API, and
`app.js` is only screens; the SDK does the rest. `overfin.js` here is a copy
of the SDK, kept identical by a test.

- **Account setup**: start fresh, or bring an existing media server account.
- **Who's watching?**: profiles, PINs, adding a profile (kids profiles with a
  rating ceiling).
- **Home**: continue watching, next up, latest per library.
- **Libraries**: sorting, a repeatable shuffle, infinite scroll with cursors.
- **Item pages**: series, seasons, episodes, cast.
- **Player**: Overfin picks direct play, repackaging or transcoding from what
  the browser declares; audio and subtitle choice, "Skip intro", progress
  reporting, resume, and a clear message when another device takes over.

Every screen lives in the address (`#/item/…`, `#/play/…`), so a reload
comes back to it; playback resumes from the position the player last
reported. Text from the server is only ever set with `textContent`.

## Running it

Serve the folder as static files on the same site as Overfin, with `/api/*`
going to Overfin. With Caddy and Overfin's own login (see the main README):

```caddyfile
example.org {
	handle /api/* {
		reverse_proxy overfin:8080
	}
	handle {
		forward_auth overfin:8080 {
			uri /api/v1/gate
		}
		root * /srv/demo-site
		file_server
	}
}
```

It needs the `library` and `playback` plugins.

## Third-party code

`vendor/hls-1.7.3.min.js` is [hls.js](https://github.com/video-dev/hls.js)
1.7.3, unmodified, under the Apache License 2.0 (`vendor/hls.js-LICENSE`).
It plays HLS streams in browsers without native support. It's served from
the site itself, so browsers never contact a third party.
