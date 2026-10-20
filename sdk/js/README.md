# Overfin JavaScript SDK

One ES module, no dependencies, no build step. It wraps Overfin's API and
carries the behavior every client needs, so your site only handles screens:

- **Login:** when the login lapses, the browser goes through it and comes
  back to the same page.
- **Errors:** every failure is an `OverfinError` with a stable `type`
  (`pin-incorrect`, `step-up-required`, …) to branch on.
- **Paging:** `pages()` follows cursors for you, and keeps a shuffle's seed.
- **Playback:** `play()` tells Overfin what the browser can play, attaches the
  stream (with [hls.js](https://github.com/video-dev/hls.js) where the browser
  lacks native HLS) and subtitles, reports progress, restarts a stream the
  server freed at the same place without a word, switches audio and
  subtitles, and reports a takeover by another device.

```js
import { Overfin } from './overfin.js';

const overfin = new Overfin();

const me = await overfin.me();                 // account and active profile
const libraries = await overfin.libraries();
const { items } = await overfin.items({ library: libraries[0].id, sort: 'addedAt', order: 'desc' });
img.src = overfin.imageUrl(items[0].images.primary, 'small');

const player = await overfin.play(videoElement, items[0].id, {
  onTakenOver: (device) => showMessage(`Playing on ${device}`),  // then player.continueHere()
  onError: (err) => showMessage(err.message),
});
player.setSubtitleTrack(player.playback.subtitleTracks[0].id);
// leaving the page:
await player.close();
```

Errors:

```js
try {
  await overfin.activateProfile(id, pin);
} catch (err) {
  if (err.type === 'pin-incorrect') …
  if (err.type === 'step-up-required') overfin.stepUp(err); // log in again, come back
}
```

Copy `overfin.js` (and `overfin.d.ts` for TypeScript) into your site. The
[overfin-web](../../web) is built on it and shows every part in
use. The API itself is specified in [api/openapi.yaml](../../api/openapi.yaml).

Tests: `node --test sdk/js/overfin.test.js`.
