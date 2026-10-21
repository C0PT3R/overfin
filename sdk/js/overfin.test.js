// Tests for overfin.js, with Node's own test runner:
//   node --test sdk/js/overfin.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Overfin, OverfinError, Player, capabilities } from './overfin.js';

// fakeServer answers fetch() from a table of handlers, and records calls.
function fakeServer(handlers) {
  const calls = [];
  globalThis.fetch = async (url, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : undefined;
    calls.push({ method: init.method, url, body });
    const key = `${init.method} ${url.split('?')[0]}`;
    const handler = handlers[key];
    const [status, data] = handler ? handler(url, body) : [404, { type: 'not-found', title: 'Not found', status: 404 }];
    return {
      status,
      ok: status >= 200 && status < 300,
      json: async () => data,
    };
  };
  return calls;
}

globalThis.location = { pathname: '/', search: '', hash: '#/item/item_1', href: '' };

test('errors are typed problems', async () => {
  fakeServer({ 'POST /api/v1/profiles/p1/activate': () => [403, { type: 'pin-incorrect', title: 'Wrong PIN', status: 403 }] });
  const overfin = new Overfin();
  await assert.rejects(overfin.activateProfile('p1', '0000'), (err) => {
    assert.ok(err instanceof OverfinError);
    assert.equal(err.type, 'pin-incorrect');
    assert.equal(err.status, 403);
    return true;
  });
});

test('a lapsed login goes through the login and back', async () => {
  fakeServer({ 'GET /api/v1/me': () => [401, { type: 'not-authenticated', status: 401 }] });
  let sentTo = '';
  const overfin = new Overfin({ onLoginRequired: (url) => { sentTo = url; } });
  await assert.rejects(overfin.me());
  assert.equal(sentTo, '/api/v1/login?return=%2F%23%2Fitem%2Fitem_1');
});

test('queries repeat list values and skip empty ones', async () => {
  const calls = fakeServer({ 'GET /api/v1/items': () => [200, { items: [] }] });
  await new Overfin().items({ kind: ['movie', 'series'], genre: '', sort: 'name', limit: 10 });
  assert.equal(calls[0].url, '/api/v1/items?kind=movie&kind=series&sort=name&limit=10');
});

test('pages follow cursors and keep the shuffle seed', async () => {
  const calls = fakeServer({
    'GET /api/v1/items': (url) => {
      const q = new URL(url, 'http://x').searchParams;
      if (!q.get('cursor')) return [200, { items: [{ id: 'a' }], nextCursor: 'c2', seed: 's1' }];
      return [200, { items: [{ id: 'b' }] }];
    },
  });
  const seen = [];
  for await (const page of new Overfin().pages({ sort: 'random' })) seen.push(...page.items.map((i) => i.id));
  assert.deepEqual(seen, ['a', 'b']);
  assert.match(calls[1].url, /cursor=c2/);
  assert.match(calls[1].url, /seed=s1/);
});

test('onWatchlist is a yes or no', async () => {
  fakeServer({
    'GET /api/v1/me/watchlist/item_on': () => [204, null],
    'GET /api/v1/me/watchlist/item_off': () => [404, { type: 'not-found', status: 404 }],
  });
  const overfin = new Overfin();
  assert.equal(await overfin.onWatchlist('item_on'), true);
  assert.equal(await overfin.onWatchlist('item_off'), false);
});

test('discovery asks for its list and filters', async () => {
  const calls = fakeServer({
    'GET /api/v1/discover': () => [200, { items: [{ tmdbId: '949', availability: 'missing' }] }],
    'GET /api/v1/catalog/movie/949': () => [200, { tmdbId: '949', genres: ['Crime'] }],
  });
  const overfin = new Overfin();
  const page = await overfin.discover('trending', 'movie', { availability: 'missing', limit: 20 });
  assert.equal(page.items[0].tmdbId, '949');
  assert.match(calls[0].url, /list=trending/);
  assert.match(calls[0].url, /mediaKind=movie/);
  assert.match(calls[0].url, /availability=missing/);
  assert.doesNotMatch(calls[0].url, /cursor=/);
  assert.deepEqual((await overfin.catalogTitle('movie', '949')).genres, ['Crime']);
});

test('marks are PUT or DELETE on the title', async () => {
  const calls = fakeServer({
    'PUT /api/v1/items/item_1/played': () => [200, { played: true }],
    'DELETE /api/v1/items/item_1/favorite': () => [200, { favorite: false }],
    'DELETE /api/v1/items/item_1/progress': () => [200, { positionMs: 0 }],
    'DELETE /api/v1/me/history': () => [204, null],
  });
  const overfin = new Overfin();
  assert.equal((await overfin.setPlayed('item_1')).played, true);
  assert.equal((await overfin.setFavorite('item_1', false)).favorite, false);
  await overfin.forgetProgress('item_1');
  assert.equal(await overfin.clearHistory(), null);
  assert.deepEqual(calls.map((c) => c.method), ['PUT', 'DELETE', 'DELETE', 'DELETE']);
});

test('no content is null', async () => {
  fakeServer({ 'DELETE /api/v1/profiles/p1/pin': () => [204, null] });
  assert.equal(await new Overfin().removePin('p1'), null);
});

// ---- Player ------------------------------------------------------------------

function fakeVideo() {
  const listeners = {};
  return {
    currentTime: 0, paused: false, autoplay: false, src: '',
    addEventListener(ev, fn) { (listeners[ev] ||= new Set()).add(fn); },
    removeEventListener(ev, fn) { listeners[ev]?.delete(fn); },
    emit(ev) { for (const fn of listeners[ev] || []) fn(); },
    querySelectorAll: () => [],
    append() {},
    canPlayType: () => '',
    removeAttribute() {},
    load() {},
    pause() { this.paused = true; },
  };
}

function playbackServer() {
  let n = 0;
  const state = { gone: null };
  const calls = fakeServer({
    'POST /api/v1/playback': (_, body) => [201, {
      id: `pb${++n}`, item: { id: body.itemId, name: 'Film' }, method: 'direct',
      stream: { url: `/api/v1/stream/t${n}/file`, protocol: 'file' }, positionMs: body.startPositionMs,
    }],
    'POST /api/v1/playback/pb1/progress': () => state.gone || [204, null],
    'POST /api/v1/playback/pb1/stop': () => [410, { type: 'playback-expired', status: 410 }],
    'POST /api/v1/playback/pb2/stop': () => [204, null],
    'GET /api/v1/playback/pb1': () => state.gone || [200, {}],
  });
  return { calls, state };
}

const settle = () => new Promise((r) => setTimeout(r, 10));

test('the player restarts a freed stream at the same place, quietly', async () => {
  const { calls, state } = playbackServer();
  const video = fakeVideo();
  const errors = [];
  const player = await new Overfin().play(video, 'item_1', {
    startMs: 0, capabilities: {}, onError: (e) => errors.push(e),
  });
  assert.equal(player.playback.id, 'pb1');

  video.currentTime = 754.2;
  state.gone = [410, { type: 'playback-expired', status: 410 }];
  video.emit('seeked'); // reports progress, which finds the playback gone
  await settle();

  assert.equal(player.playback.id, 'pb2');
  const restart = calls.filter((c) => c.method === 'POST' && c.url === '/api/v1/playback')[1];
  assert.equal(restart.body.startPositionMs, 754200);
  assert.deepEqual(errors, []);
  await player.close();
});

test('a takeover is reported, and continueHere takes it back', async () => {
  const { state } = playbackServer();
  const video = fakeVideo();
  let takenBy = null;
  const player = await new Overfin().play(video, 'item_1', {
    startMs: 0, capabilities: {}, onTakenOver: (device) => { takenBy = device; },
  });
  video.currentTime = 30;
  state.gone = [410, { type: 'playback-taken-over', takenOverBy: 'Firefox on Linux', status: 410 }];
  video.emit('pause');
  await settle();
  assert.equal(takenBy, 'Firefox on Linux');
  assert.ok(video.paused);

  await player.continueHere();
  assert.equal(player.playback.id, 'pb2');
  await player.close();
});

test('without a start position, the player resumes where the profile left off', async () => {
  const calls = fakeServer({
    'GET /api/v1/items/item_1': () => [200, { id: 'item_1', userState: { positionMs: 42000 } }],
    'POST /api/v1/playback': (_, body) => [201, {
      id: 'pb1', item: { id: 'item_1', name: 'Film' }, method: 'direct',
      stream: { url: '/x', protocol: 'file' }, positionMs: body.startPositionMs,
    }],
    'POST /api/v1/playback/pb1/stop': () => [204, null],
  });
  const player = await new Overfin().play(fakeVideo(), 'item_1', { capabilities: {} });
  assert.equal(calls.find((c) => c.url === '/api/v1/playback').body.startPositionMs, 42000);
  await player.close();
});

test('Player is exported for clients that build their own', () => {
  assert.equal(typeof Player, 'function');
});

// A browser, as capabilities() sees it: its name, and what it says it
// plays. The screen is a standard one.
function asBrowser(userAgent, answers) {
  Object.defineProperty(globalThis, 'navigator', { value: { userAgent }, configurable: true });
  globalThis.document = { createElement: () => ({ canPlayType: (mime) => answers[mime] || '' }) };
  globalThis.screen = { width: 3840, height: 2160 };
  globalThis.matchMedia = () => ({ matches: false });
  globalThis.MediaSource = undefined;
}

const plays = {
  'video/mp4; codecs="avc1.640028"': 'probably',
  'video/mp4; codecs="hvc1.2.4.L153.90"': 'probably', // Main 10 only
  'video/x-matroska': 'maybe',
};

test('Chrome gets Matroska, 10-bit HEVC and HDR, whatever the screen', () => {
  asBrowser('Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36', plays);
  const c = capabilities();
  assert.ok(c.containers.includes('mkv'), c.containers);
  assert.ok(c.videoCodecs.includes('hevc'), c.videoCodecs);
  assert.deepEqual(c.hdr, ['hdr10', 'hlg']);
});

test('Firefox gets neither Matroska nor HDR on Linux, whatever it says', () => {
  asBrowser('Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0', plays);
  const c = capabilities();
  assert.ok(!c.containers.includes('mkv'), c.containers);
  assert.deepEqual(c.hdr, []);
});

