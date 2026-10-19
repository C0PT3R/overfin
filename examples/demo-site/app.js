// Overfin demo site: a small, complete client of the Overfin API, in plain
// JavaScript. Everything shown comes from /api/v1; the browser never talks
// to Jellyfin. Text from the server is only ever set with textContent, never
// parsed as HTML.
'use strict';

// ---- Helpers ---------------------------------------------------------------

const $ = (sel) => document.querySelector(sel);

// h builds an element: h('a', {href: '#/', class: 'x'}, 'text', child).
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat()) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

function toast(text) {
  const t = $('#toast');
  t.textContent = text;
  t.classList.add('show');
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => t.classList.remove('show'), 4000);
}

function duration(ms) {
  const m = Math.round(ms / 60000);
  return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
}

function clock(ms) {
  const s = Math.floor(ms / 1000);
  const hh = Math.floor(s / 3600), mm = Math.floor(s / 60) % 60, ss = s % 60;
  const two = (n) => String(n).padStart(2, '0');
  return hh ? `${hh}:${two(mm)}:${two(ss)}` : `${mm}:${two(ss)}`;
}

// ---- API -------------------------------------------------------------------

class ApiError extends Error {
  constructor(status, problem) {
    super(problem.detail || problem.title || `HTTP ${status}`);
    this.status = status;
    this.problem = problem;
  }
}

// api calls Overfin. Errors are RFC 9457 problems; clients branch on their
// type. Not logged in (any more): go through the login and come back here.
async function api(method, path, body, opts = {}) {
  const init = { method, headers: {}, keepalive: opts.keepalive };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch('/api/v1' + path, init);
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    if (data.type === 'not-authenticated') {
      location.href = '/api/v1/login?return=' + encodeURIComponent(location.pathname + location.hash);
    }
    throw new ApiError(res.status, data);
  }
  return data;
}

// Messages for the problems a user can meet, keyed by type.
const messages = {
  'pin-incorrect': 'Wrong PIN.',
  'pin-required': 'This profile is protected by a PIN.',
  'pin-locked': 'Too many wrong PINs. Ask the account owner.',
  'rate-limited': 'Too many attempts; wait a moment.',
  'claim-failed': 'Wrong username or password.',
  'claim-not-allowed': "This account can't be brought over.",
  'stream-limit-reached': 'Too many streams on this account.',
  'server-busy': 'The server is busy; try again shortly.',
  'not-playable': "This item can't be played.",
  'name-taken': 'That name is already used.',
  'profile-limit-reached': 'This account has as many profiles as allowed.',
  'owner-only': 'Only the account owner can do this.',
};

function explain(err) {
  const p = err.problem || {};
  let text = messages[p.type] || p.detail || p.title || 'Something went wrong.';
  if (p.retryAfter) text += ` (${p.retryAfter} s)`;
  return text;
}

// ---- State -----------------------------------------------------------------

const state = {
  me: null,         // GET /me
  libraries: [],    // GET /libraries
};

const imageURL = (ref, size) => ref && ref.url + '?size=' + size;

// ---- Layout ----------------------------------------------------------------

function render(...nodes) {
  const main = $('#main');
  main.replaceChildren(...nodes);
  window.scrollTo(0, 0);
}

function renderHeader() {
  $('#top').hidden = false;
  const route = location.hash;
  $('#nav').replaceChildren(
    h('a', { href: '#/', class: route === '' || route === '#/' ? 'active' : '' }, 'Home'),
    ...state.libraries.map((lib) =>
      h('a', { href: `#/library/${lib.id}`, class: route.startsWith(`#/library/${lib.id}`) ? 'active' : '' }, lib.name)),
  );
  $('#who').textContent = state.me.activeProfile.name;
}

// card is one item in a row or grid: poster, title, progress.
function card(item, wide = false) {
  const img = wide ? (item.images?.thumb || item.images?.backdrops?.[0] || item.images?.primary) : item.images?.primary;
  const poster = h('div', { class: wide ? 'poster wide' : 'poster' });
  if (img) {
    poster.append(h('img', { src: imageURL(img, wide ? 'small' : 'small'), alt: '', loading: 'lazy' }));
  } else {
    poster.append(h('div', { class: 'fallback' }, item.name));
  }
  const pos = item.userState?.positionMs;
  if (pos && item.durationMs) {
    const bar = h('div', { class: 'progress' });
    bar.style.width = Math.min(100, (100 * pos) / item.durationMs) + '%';
    poster.append(bar);
  }
  let sub = item.year || '';
  if (item.episode) {
    const e = item.episode;
    sub = `${e.seriesName || ''} · S${e.seasonNumber ?? '?'}E${e.number ?? '?'}`;
  }
  return h('a', { class: 'card', href: `#/item/${item.id}` },
    poster, h('div', { class: 'title' }, item.name), h('div', { class: 'sub' }, sub));
}

function row(title, items, wide = false) {
  if (!items.length) return null;
  return [h('h2', {}, title), h('div', { class: 'row' }, items.map((i) => card(i, wide)))];
}

// ---- Screens ---------------------------------------------------------------

async function home() {
  const [resume, nextUp] = await Promise.all([
    api('GET', '/items/resume?limit=12'),
    api('GET', '/items/next-up?limit=12'),
  ]);
  const latest = await Promise.all(state.libraries.map((lib) =>
    api('GET', `/items/latest?library=${lib.id}&limit=16`).then((r) => [lib, r.items])));
  const parts = [
    row('Continue watching', resume.items, true),
    row('Next up', nextUp.items, true),
    ...latest.map(([lib, items]) => row(`Latest in ${lib.name}`, items)),
  ].flat().filter(Boolean);
  render(...(parts.length ? parts : [h('p', { class: 'muted' }, 'Nothing here yet.')]));
}

// Which items a library page lists, by library kind.
const libraryKinds = { movies: 'movie', shows: 'series', musicVideos: 'musicVideo', music: 'musicAlbum', books: 'book' };

async function libraryPage(id, params) {
  const lib = state.libraries.find((l) => l.id === id);
  if (!lib) return notFound();
  const sort = params.get('sort') || 'name';
  const grid = h('div', { class: 'grid' });
  const select = h('select', {
    onchange: () => { location.hash = `#/library/${id}?sort=${select.value}`; },
  }, [['name', 'Name'], ['addedAt', 'Recently added'], ['premiereDate', 'Release date'], ['communityRating', 'Rating'], ['random', 'Shuffle']]
    .map(([v, label]) => h('option', { value: v, selected: v === sort }, label)));
  const count = h('span', { class: 'muted' });
  const sentinel = h('div');
  render(h('h1', {}, lib.name), h('div', { class: 'toolbar' }, select, count), grid, sentinel);

  const q = new URLSearchParams({ library: id, sort, limit: '60' });
  if (libraryKinds[lib.kind]) q.set('kind', libraryKinds[lib.kind]);
  if (sort === 'addedAt' || sort === 'premiereDate' || sort === 'communityRating') q.set('order', 'desc');
  // A shuffle keeps its seed in the address, so a reload shows the same order.
  if (sort === 'random' && params.get('seed')) q.set('seed', params.get('seed'));

  let cursor = '', loading = false, done = false;
  const route = location.hash;
  async function more() {
    if (loading || done || location.hash.split('&seed=')[0] !== route.split('&seed=')[0]) return;
    loading = true;
    const page = await api('GET', '/items?' + q + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''));
    grid.append(...page.items.map((i) => card(i)));
    if (page.totalCount !== undefined) count.textContent = `${page.totalCount} items`;
    if (page.seed && !params.get('seed')) {
      q.set('seed', page.seed);
      history.replaceState(null, '', `#/library/${id}?sort=random&seed=${page.seed}`);
    }
    cursor = page.nextCursor || '';
    done = !cursor;
    loading = false;
    if (!done && sentinel.getBoundingClientRect().top < innerHeight * 2) more();
  }
  new IntersectionObserver((entries) => { if (entries[0].isIntersecting) more(); },
    { rootMargin: '800px' }).observe(sentinel);
  await more();
}

const playableKinds = new Set(['movie', 'episode', 'video', 'musicVideo']);

async function itemPage(id) {
  const item = await api('GET', `/items/${id}`);
  const backdrop = item.images?.backdrops?.[0];
  const hero = h('div', { class: 'hero' });
  if (backdrop) hero.append(h('img', { class: 'backdrop', src: imageURL(backdrop, 'large'), alt: '' }));
  const poster = h('div', { class: 'poster' });
  if (item.images?.primary) poster.append(h('img', { src: imageURL(item.images.primary, 'medium'), alt: '' }));

  const meta = h('div', { class: 'meta' },
    item.year && h('span', {}, item.year),
    item.officialRating && h('span', {}, item.officialRating),
    item.durationMs && h('span', {}, duration(item.durationMs)),
    item.communityRating && h('span', {}, '★ ' + item.communityRating.toFixed(1)),
    item.genres?.length && h('span', {}, item.genres.join(', ')));

  const actions = h('div', { class: 'actions' });
  if (playableKinds.has(item.kind)) {
    const pos = item.userState?.positionMs || 0;
    if (pos) {
      actions.append(h('button', { class: 'primary', onclick: () => play(item.id, pos) }, `Resume from ${clock(pos)}`));
      actions.append(h('button', { onclick: () => play(item.id, 0) }, 'From the start'));
    } else {
      actions.append(h('button', { class: 'primary', onclick: () => play(item.id, 0) }, 'Play'));
    }
  }

  let parentLink = null;
  if (item.episode) {
    const e = item.episode;
    parentLink = h('p', {}, h('a', { href: `#/item/${e.seriesId}` }, e.seriesName || 'Series'),
      ` · Season ${e.seasonNumber ?? '?'}, episode ${e.number ?? '?'}`);
  } else if (item.season) {
    parentLink = h('p', {}, h('a', { href: `#/item/${item.season.seriesId}` }, item.season.seriesName || 'Series'));
  }

  hero.append(h('div', { class: 'info' }, poster,
    h('div', {}, h('h1', {}, item.name), parentLink, meta, actions,
      item.overview && h('p', {}, item.overview))));

  const below = [];
  if (item.kind === 'series' || item.kind === 'season') {
    const childKind = item.kind === 'series' ? 'season' : 'episode';
    const children = await api('GET', `/items?parent=${item.id}&kind=${childKind}&sort=name&limit=100`);
    below.push(h('h2', {}, childKind === 'season' ? 'Seasons' : 'Episodes'),
      h('div', { class: childKind === 'season' ? 'row' : 'grid' }, children.items.map((i) => card(i, childKind === 'episode'))));
  }
  const cast = (item.credits || []).filter((c) => c.role === 'actor').slice(0, 12);
  if (cast.length) {
    below.push(h('h2', {}, 'Cast'), h('p', { class: 'muted' },
      cast.map((c) => c.person.name + (c.character ? ` (${c.character})` : '')).join(', ')));
  }
  render(hero, ...below);
}

function notFound() {
  render(h('div', { class: 'center' }, h('h1', {}, 'Not found'), h('a', { href: '#/' }, 'Back home')));
}

// ---- Account setup -----------------------------------------------------------

function setupPage() {
  $('#top').hidden = true;
  const msg = h('p', { class: 'error' });
  const username = h('input', { name: 'username', autocomplete: 'username', required: true });
  const password = h('input', { name: 'password', type: 'password', autocomplete: 'current-password', required: true });
  const claim = h('form', { class: 'stack', onsubmit: async (ev) => {
    ev.preventDefault();
    msg.textContent = '';
    try {
      await api('POST', '/me/claim', { username: username.value, password: password.value });
      password.value = '';
      start();
    } catch (err) {
      msg.textContent = err.status === 404 ? 'Bringing an existing account is not offered here.' : explain(err);
    }
  } }, h('label', {}, 'Media server username', username), h('label', {}, 'Password', password),
  h('button', { type: 'submit' }, 'Bring my account'));

  render(h('div', { class: 'center' },
    h('h1', {}, 'Welcome'),
    h('div', { class: 'panel' },
      h('p', {}, 'Start with a fresh account.'),
      h('button', { class: 'primary', onclick: async () => {
        try { await api('POST', '/me', {}); start(); } catch (err) { msg.textContent = explain(err); }
      } }, 'Create my account')),
    h('div', { class: 'panel' },
      h('p', {}, 'Or bring your existing media server account, with its watch history. Your password is used once and not kept.'),
      claim),
    msg));
}

// ---- Profiles ----------------------------------------------------------------

async function profilesPage() {
  $('#top').hidden = true;
  const { items } = await api('GET', '/profiles');
  const msg = h('p', { class: 'error' });
  const pinArea = h('div');

  async function choose(p, pin) {
    msg.textContent = '';
    try {
      await api('POST', `/profiles/${p.id}/activate`, pin ? { pin } : {});
      sessionStorage.setItem('overfin-demo-picked', '1');
      state.me = await api('GET', '/me');
      location.hash = '#/'; // the hashchange shows home
    } catch (err) {
      if (err.problem.type === 'pin-required') return askPIN(p);
      msg.textContent = explain(err);
    }
  }

  function askPIN(p) {
    const input = h('input', { inputmode: 'numeric', autocomplete: 'off', pattern: '[0-9]{4,8}', maxlength: '8', required: true, 'aria-label': 'PIN' });
    pinArea.replaceChildren(h('form', { class: 'center stack', onsubmit: (ev) => { ev.preventDefault(); choose(p, input.value); input.value = ''; } },
      h('p', {}, `PIN for ${p.name}`), input, h('button', { class: 'primary', type: 'submit' }, 'OK')));
    input.focus();
  }

  const add = state.me.activeProfile.owner &&
    h('button', { class: 'profile', onclick: () => newProfile() }, h('div', { class: 'avatar' }, '+'), 'Add profile');

  render(h('h1', { class: 'center' }, "Who's watching?"),
    h('div', { class: 'profiles' },
      items.map((p) => h('button', { class: 'profile', onclick: () => (p.hasPin ? askPIN(p) : choose(p)) },
        h('div', { class: 'avatar' }, p.name.slice(0, 1).toUpperCase()), p.name,
        h('div', { class: 'badge' }, [p.kind === 'kids' ? `Kids · ${p.ratingCeiling}` : '', p.hasPin ? '🔒' : ''].filter(Boolean).join(' ')))),
      add),
    pinArea, h('div', { class: 'center' }, msg));
}

async function newProfile() {
  const { items: ratings } = await api('GET', '/ratings');
  const name = h('input', { maxlength: '32', required: true });
  const kids = h('input', { type: 'checkbox' });
  const ceiling = h('select', {}, ratings.map((r) => h('option', { value: r.name }, r.name)));
  const msg = h('p', { class: 'error' });
  render(h('div', { class: 'center' }, h('h1', {}, 'New profile'),
    h('form', { class: 'stack panel', onsubmit: async (ev) => {
      ev.preventDefault();
      const body = { name: name.value };
      if (kids.checked) Object.assign(body, { kind: 'kids', ratingCeiling: ceiling.value });
      try { await api('POST', '/profiles', body); profilesPage(); } catch (err) { msg.textContent = explain(err); }
    } },
    h('label', {}, 'Name', name),
    h('label', {}, kids, ' Kids profile, up to rating ', ceiling),
    h('button', { class: 'primary', type: 'submit' }, 'Create'),
    h('button', { type: 'button', onclick: () => profilesPage() }, 'Cancel')),
    msg));
}

// ---- Playback ------------------------------------------------------------------

// capabilities tells Overfin what this browser can play, so it picks direct
// play, repackaging or transcoding.
function capabilities() {
  const video = document.createElement('video');
  const mse = window.ManagedMediaSource || window.MediaSource;
  const can = (mime) => Boolean((mse && mse.isTypeSupported(mime)) || video.canPlayType(mime));
  const pick = (table) => Object.entries(table).filter(([, mime]) => can(mime)).map(([name]) => name);
  const pixels = Math.max(screen.width, screen.height) * (window.devicePixelRatio || 1);
  return {
    containers: pick({ mp4: 'video/mp4; codecs="avc1.640028"', webm: 'video/webm; codecs="vp9"' }),
    videoCodecs: pick({
      h264: 'video/mp4; codecs="avc1.640028"',
      hevc: 'video/mp4; codecs="hvc1.1.6.L120.90"',
      av1: 'video/mp4; codecs="av01.0.08M.08"',
      vp9: 'video/mp4; codecs="vp09.00.10.08"',
    }),
    audioCodecs: pick({
      aac: 'audio/mp4; codecs="mp4a.40.2"',
      mp3: 'audio/mpeg',
      opus: 'audio/mp4; codecs="opus"',
      flac: 'audio/mp4; codecs="flac"',
      ac3: 'audio/mp4; codecs="ac-3"',
      eac3: 'audio/mp4; codecs="ec-3"',
    }),
    maxHeight: pixels >= 3000 ? 2160 : 1080,
    hdr: matchMedia('(dynamic-range: high)').matches ? ['hdr10'] : [],
    subtitles: ['vtt'],
    maxAudioChannels: 2,
  };
}

// The playback on screen, if any, so leaving the page can stop it.
let current = null;

// Where the next playback starts, when the user just chose (resume, or from
// the start). After a reload there's no choice: the server's last known
// position is used, as reported during playback.
let chosenStart = null;

function play(itemId, positionMs) {
  chosenStart = positionMs;
  location.hash = `#/play/${itemId}`;
}

async function playerPage(itemId) {
  let startMs = chosenStart;
  chosenStart = null;
  if (startMs === null) {
    const item = await api('GET', `/items/${itemId}`);
    startMs = item.userState?.positionMs || 0;
  }
  const video = h('video', { controls: true, autoplay: true, playsinline: true });
  const title = h('div', { class: 'title' });
  const audio = h('select', { 'aria-label': 'Audio' });
  const subs = h('select', { 'aria-label': 'Subtitles' });
  const skip = h('button', { class: 'skip', hidden: true }, 'Skip intro');
  const box = h('div', { class: 'player' }, video, skip,
    h('div', { class: 'bar' },
      h('button', { onclick: () => history.back() }, '← Back'), title, audio, subs));
  render(box);
  await startStream({ itemId, positionMs: startMs, choice: {}, video, title, audio, subs, skip, box });
}

// startStream starts (or restarts) the playback described by ctx: the item,
// where to start, and the audio and subtitle choice.
async function startStream(ctx, choice) {
  const { itemId, video, title, audio, subs, skip, box } = ctx;
  if (choice) ctx.choice = { ...ctx.choice, ...choice };
  await stopCurrent();
  box.querySelector('.notice')?.remove();
  let pb;
  try {
    pb = await api('POST', '/playback', {
      itemId, capabilities: capabilities(), startPositionMs: Math.round(ctx.positionMs), ...ctx.choice,
    });
  } catch (err) {
    box.append(h('div', { class: 'notice' }, h('div', {}, h('p', {}, explain(err)),
      h('button', { onclick: () => history.back() }, 'Back'))));
    return;
  }
  const me = { pb, ctx, video, hls: null, timer: null, stopped: false, pausedAt: 0 };
  current = me;
  title.textContent = pb.item.name + (pb.method !== 'direct' ? ` · ${pb.method}` : '');

  // Audio: changing it restarts the stream at the same place.
  audio.replaceChildren(...(pb.audioTracks || []).map((t) => h('option', { value: t.id, selected: t.id === pb.audioTrack }, t.label)));
  audio.hidden = (pb.audioTracks || []).length < 2;
  audio.onchange = () => { ctx.positionMs = video.currentTime * 1000; startStream(ctx, { audioTrack: audio.value }); };

  // Subtitles: text tracks come as WebVTT and the browser shows them; others
  // are burned in by restarting the stream with them.
  for (const old of video.querySelectorAll('track')) old.remove();
  const tracks = pb.subtitleTracks || [];
  subs.replaceChildren(h('option', { value: '' }, 'No subtitles'),
    ...tracks.map((t) => h('option', { value: t.id, selected: t.id === pb.subtitleTrack }, t.label)));
  subs.hidden = tracks.length === 0;
  for (const t of tracks.filter((t) => t.url)) {
    video.append(h('track', { kind: 'subtitles', src: t.url, srclang: t.language || 'und', label: t.label, 'data-id': t.id }));
  }
  subs.onchange = () => {
    const t = tracks.find((x) => x.id === subs.value);
    if (t && !t.url) { ctx.positionMs = video.currentTime * 1000; startStream(ctx, { subtitleTrack: t.id }); return; }
    for (const tt of video.textTracks) tt.mode = 'disabled';
    const el = [...video.querySelectorAll('track')].find((x) => x.dataset.id === subs.value);
    if (el) el.track.mode = 'showing';
  };

  // The stream itself.
  const startAt = (pb.positionMs || 0) / 1000;
  if (pb.stream.protocol === 'hls' && window.Hls && Hls.isSupported()) {
    me.hls = new Hls({ startPosition: startAt });
    me.hls.on(Hls.Events.ERROR, (_, data) => { if (data.fatal) checkEnded(me); });
    me.hls.loadSource(pb.stream.url);
    me.hls.attachMedia(video);
  } else if (pb.stream.protocol === 'hls' && !video.canPlayType('application/vnd.apple.mpegurl')) {
    box.append(h('div', { class: 'notice' }, h('p', {}, "This browser can't play this stream.")));
    return;
  } else {
    video.src = pb.stream.url;
    video.addEventListener('loadedmetadata', () => { if (startAt) video.currentTime = startAt; }, { once: true });
  }

  // "Skip intro" while inside an intro segment.
  const intro = (pb.segments || []).find((s) => s.kind === 'intro');
  skip.onclick = () => { if (intro) video.currentTime = intro.endMs / 1000; };
  video.ontimeupdate = () => {
    const ms = video.currentTime * 1000;
    skip.hidden = !(intro && ms >= intro.startMs && ms < intro.endMs - 1000);
  };

  // Progress: every 10 seconds, and on pause, seek and end.
  const report = (extra = {}) => {
    if (me.stopped) return;
    api('POST', `/playback/${pb.id}/progress`, { positionMs: Math.round(video.currentTime * 1000), paused: video.paused, ...extra })
      .catch((err) => ended(me, err));
  };
  me.timer = setInterval(() => { if (!video.paused) report(); }, 10000);
  video.onpause = () => { me.pausedAt = Date.now(); report(); };
  // After a long pause the server has likely freed the stream; check, and
  // recover at once if so.
  video.onplay = () => {
    if (me.pausedAt && Date.now() - me.pausedAt > 90000) checkEnded(me);
    me.pausedAt = 0;
  };
  video.onseeked = () => report();
  video.onended = () => { report({ ended: true }); stopCurrent(); };
}

// ended handles a playback the server no longer has. Freed after a pause or
// a lost connection: start again at the same place, without a word. Taken
// over by another device: say so, and offer to continue here.
function ended(me, err) {
  if (me !== current || err.status !== 410) return;
  const { ctx, video } = me;
  const p = err.problem;
  ctx.positionMs = video.currentTime * 1000;
  if (p.type === 'playback-expired') {
    const wasPlaying = !video.paused;
    cleanup(me);
    current = null;
    video.autoplay = wasPlaying;
    startStream(ctx);
    return;
  }
  cleanup(me);
  current = null;
  video.pause();
  const text = p.type === 'playback-taken-over'
    ? `Playback continued on ${p.takenOverBy || 'another device'}.`
    : explain(err);
  ctx.box.append(h('div', { class: 'notice' }, h('div', {}, h('p', {}, text),
    h('div', { class: 'actions' },
      h('button', { class: 'primary', onclick: () => { video.autoplay = true; startStream(ctx); } }, 'Continue here'),
      h('button', { onclick: () => history.back() }, 'Back')))));
}

async function checkEnded(me) {
  try { await api('GET', `/playback/${me.pb.id}`); } catch (err) { ended(me, err); }
}

function cleanup(me) {
  me.stopped = true;
  clearInterval(me.timer);
  if (me.hls) me.hls.destroy();
}

// stopCurrent reports the final position and stops the playback, so the
// server frees the stream at once.
async function stopCurrent() {
  const me = current;
  if (!me || me.stopped) return;
  current = null;
  const positionMs = Math.round(me.video.currentTime * 1000);
  cleanup(me);
  me.video.removeAttribute('src');
  me.video.load();
  await api('POST', `/playback/${me.pb.id}/stop`, { positionMs }, { keepalive: true }).catch(() => {});
}

// ---- Routing -------------------------------------------------------------------

// Every screen lives in the address, so a reload comes back to it.
async function route() {
  const [path, query] = location.hash.slice(1).split('?');
  const params = new URLSearchParams(query || '');
  const parts = (path || '/').split('/').filter(Boolean);
  if (parts[0] !== 'play') await stopCurrent();
  try {
    if (parts[0] === 'profiles') return await profilesPage();
    renderHeader();
    switch (parts[0]) {
      case undefined: return await home();
      case 'library': return await libraryPage(parts[1], params);
      case 'item': return await itemPage(parts[1]);
      case 'play': return await playerPage(parts[1]);
      default: return notFound();
    }
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return notFound();
    console.error(err);
    render(h('div', { class: 'center' }, h('p', { class: 'error' }, explain(err)), h('a', { href: '#/' }, 'Back home')));
  }
}

async function start() {
  try {
    state.me = await api('GET', '/me');
  } catch (err) {
    if (err.problem?.type === 'account-setup-required') return setupPage();
    if (err.problem?.type === 'account-not-allowed') {
      return render(h('div', { class: 'center' }, h('h1', {}, 'No access'), h('p', {}, explain(err))));
    }
    return render(h('p', { class: 'error' }, explain(err)));
  }
  const [{ items: libs }, { items: profiles }] = await Promise.all([
    api('GET', '/libraries').catch(() => ({ items: [] })),
    api('GET', '/profiles'),
  ]);
  state.libraries = libs;
  // "Who's watching?" once per tab, when there's a choice.
  if (profiles.length > 1 && !sessionStorage.getItem('overfin-demo-picked') &&
      !location.hash.startsWith('#/play/') && location.hash !== '#/profiles') {
    location.hash = '#/profiles'; // the hashchange shows it
    return;
  }
  route();
}

$('#who').addEventListener('click', () => { location.hash = '#/profiles'; });
$('#logout').addEventListener('click', async () => {
  await stopCurrent();
  sessionStorage.removeItem('overfin-demo-picked');
  const out = await api('POST', '/logout', {}).catch(() => ({}));
  location.href = out.logoutUrl || '/';
});
window.addEventListener('hashchange', route);
window.addEventListener('pagehide', () => { stopCurrent(); });
start();
