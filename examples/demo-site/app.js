// Overfin demo site: a small, complete website on the Overfin JavaScript
// SDK. The SDK does the talking to /api/v1 (and the login, errors, and
// keeping playback going); this file is only screens. Text from the server
// is only ever set with textContent, never parsed as HTML.
import { Overfin, OverfinError } from './overfin.js';

const overfin = new Overfin();

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

// Messages for the errors a user can meet, by type.
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
  if (!(err instanceof OverfinError)) return err?.message || 'Something went wrong.';
  let text = messages[err.type] || err.message;
  if (err.retryAfter) text += ` (${err.retryAfter} s)`;
  return text;
}

// ---- State and layout ------------------------------------------------------

const state = { me: null, libraries: [] };

function render(...nodes) {
  $('#main').replaceChildren(...nodes);
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
  if (img) poster.append(h('img', { src: overfin.imageUrl(img, 'small'), alt: '', loading: 'lazy' }));
  else poster.append(h('div', { class: 'fallback' }, item.name));
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
  const [resume, nextUp, latest] = await Promise.all([
    overfin.resume(12),
    overfin.nextUp(12),
    Promise.all(state.libraries.map(async (lib) => [lib, await overfin.latest(lib.id, 16)])),
  ]);
  const parts = [
    row('Continue watching', resume, true),
    row('Next up', nextUp, true),
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

  const query = {
    library: id, sort, limit: 60, kind: libraryKinds[lib.kind],
    order: ['addedAt', 'premiereDate', 'communityRating'].includes(sort) ? 'desc' : undefined,
    // A shuffle keeps its seed in the address, so a reload shows the same order.
    seed: sort === 'random' ? params.get('seed') || undefined : undefined,
  };
  const pages = overfin.pages(query);
  const here = location.hash.split('&seed=')[0];
  let loading = false;
  async function more() {
    if (loading || location.hash.split('&seed=')[0] !== here) return;
    loading = true;
    const { value: page, done } = await pages.next();
    loading = false;
    if (done) return observer.disconnect();
    grid.append(...page.items.map((i) => card(i)));
    if (page.totalCount !== undefined) count.textContent = `${page.totalCount} items`;
    if (page.seed && !query.seed) {
      query.seed = page.seed;
      history.replaceState(null, '', `#/library/${id}?sort=random&seed=${page.seed}`);
    }
    if (!page.nextCursor) return observer.disconnect();
    if (sentinel.getBoundingClientRect().top < innerHeight * 2) more();
  }
  const observer = new IntersectionObserver((entries) => { if (entries[0].isIntersecting) more(); },
    { rootMargin: '800px' });
  observer.observe(sentinel);
  await more();
}

const playableKinds = new Set(['movie', 'episode', 'video', 'musicVideo']);

async function itemPage(id) {
  const item = await overfin.item(id);
  const backdrop = item.images?.backdrops?.[0];
  const hero = h('div', { class: 'hero' });
  if (backdrop) hero.append(h('img', { class: 'backdrop', src: overfin.imageUrl(backdrop, 'large'), alt: '' }));
  const poster = h('div', { class: 'poster' });
  if (item.images?.primary) poster.append(h('img', { src: overfin.imageUrl(item.images.primary, 'medium'), alt: '' }));

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
    const { items } = await overfin.items({ parent: item.id, kind: childKind, sort: 'name', limit: 100 });
    below.push(h('h2', {}, childKind === 'season' ? 'Seasons' : 'Episodes'),
      h('div', { class: childKind === 'season' ? 'row' : 'grid' }, items.map((i) => card(i, childKind === 'episode'))));
  }
  const cast = (item.credits || []).filter((c) => c.role === 'actor' || c.role === 'guestStar').slice(0, 20);
  if (cast.length) {
    below.push(h('h2', {}, 'Cast'), h('div', { class: 'row' }, cast.map((c) => personCard(c.person, c.character))));
  }
  const crew = (item.credits || []).filter((c) => c.role === 'director' || c.role === 'writer');
  if (crew.length) {
    below.push(h('p', { class: 'muted' }, ...crew.flatMap((c, i) => [
      i ? ' · ' : '', `${c.role === 'director' ? 'Directed' : 'Written'} by `,
      h('a', { href: `#/person/${c.person.id}` }, c.person.name)])));
  }
  render(hero, ...below);
}

function personCard(person, sub) {
  const photo = h('div', { class: 'poster' });
  if (person.image) photo.append(h('img', { src: overfin.imageUrl(person.image, 'small'), alt: '', loading: 'lazy' }));
  else photo.append(h('div', { class: 'fallback' }, person.name));
  return h('a', { class: 'card', href: `#/person/${person.id}` },
    photo, h('div', { class: 'title' }, person.name), h('div', { class: 'sub' }, sub || ''));
}

// A person's page: who they are, and what of theirs this profile can watch.
async function personPage(id) {
  const [person, work] = await Promise.all([
    overfin.person(id),
    overfin.items({ person: id, kind: ['movie', 'series'], sort: 'premiereDate', order: 'desc', limit: 100 }),
  ]);
  const photo = h('div', { class: 'poster' });
  if (person.image) photo.append(h('img', { src: overfin.imageUrl(person.image, 'medium'), alt: '' }));
  const life = person.birthDate && (person.birthDate.slice(0, 4) + (person.deathDate ? ` – ${person.deathDate.slice(0, 4)}` : ''));
  render(
    h('div', { class: 'hero' }, h('div', { class: 'info' }, photo,
      h('div', {}, h('h1', {}, person.name), life && h('div', { class: 'meta' }, life),
        person.overview && h('p', {}, person.overview)))),
    h('h2', {}, 'In the library'),
    work.items.length ? h('div', { class: 'grid' }, work.items.map((i) => card(i))) : h('p', { class: 'muted' }, 'Nothing here.'));
}

// The account screen: download your data, delete your account. Both need a
// fresh login; the SDK sends the browser through it and back here.
function accountPage() {
  const msg = h('p', { class: 'error' });
  const owner = state.me.activeProfile.owner;
  async function withFreshLogin(action) {
    msg.textContent = '';
    try {
      await action();
    } catch (err) {
      if (err.type === 'step-up-required' && err.problem.stepUpUrl) return overfin.stepUp(err);
      if (err.type === 'pin-required') return withFreshLogin(() => action(prompt('Your PIN') || ''));
      msg.textContent = explain(err);
    }
  }
  const download = h('button', { onclick: () => withFreshLogin(async (pin) => {
    const data = await overfin.exportData(pin);
    const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' }));
    h('a', { href: url, download: 'my-data.json' }).click();
    setTimeout(() => URL.revokeObjectURL(url), 10000);
  }) }, 'Download my data');
  const remove = owner && h('button', { onclick: () => withFreshLogin(async () => {
    if (!confirm('Delete your account, all its profiles and their watch history? This cannot be undone.')) return;
    await closePlayer();
    const out = await overfin.deleteAccount();
    sessionStorage.clear();
    alert(out.history === 'kept'
      ? 'Your account is deleted. Your watch history stays on the server, unusable.'
      : 'Your account and your watch history are deleted.');
    location.href = '/';
  }) }, 'Delete my account');
  const kids = state.me.activeProfile.kind === 'kids';
  render(h('div', { class: 'center' }, h('h1', {}, 'Account'),
    !kids && h('div', { class: 'panel' },
      h('p', {}, owner ? 'Everything this service holds about your account, with your watch history.'
        : 'Everything this service holds about this profile, with its watch history.'), download),
    remove && h('div', { class: 'panel' }, h('p', {}, 'Delete your account, its profiles and their history.'), remove),
    msg));
}

function notFound() {
  render(h('div', { class: 'center' }, h('h1', {}, 'Not found'), h('a', { href: '#/' }, 'Back home')));
}

// ---- Account setup -----------------------------------------------------------

// setupPage offers a fresh account and, when the server allows it (claim is
// 'move' or 'link'), bringing an account from the service this site replaces.
// Users may never have heard of the software underneath, so the wording is
// about their previous account; a real site names its old service.
function setupPage(claim) {
  $('#top').hidden = true;
  const msg = h('p', { class: 'error' });
  const username = h('input', { name: 'username', autocomplete: 'username', required: true });
  const password = h('input', { name: 'password', type: 'password', autocomplete: 'current-password', required: true });
  const claimForm = h('form', { class: 'stack', onsubmit: async (ev) => {
    ev.preventDefault();
    msg.textContent = '';
    try {
      await overfin.claimAccount(username.value, password.value);
      password.value = '';
      start();
    } catch (err) {
      msg.textContent = err.status === 404 ? 'Bringing an existing account is not offered here.' : explain(err);
    }
  } }, h('label', {}, 'Username', username), h('label', {}, 'Password', password),
  h('button', { type: 'submit' }, 'Bring my account'));

  const bring = claim && h('div', { class: 'panel' },
    h('p', {}, 'Already had an account with us before? Bring it, with your watch history. Your password is used once and not kept.'),
    claim === 'move' && h('p', { class: 'muted' },
      'This is a move: afterwards, your old login and the apps you used with it stop working, and you watch here.'),
    claimForm);
  render(h('div', { class: 'center' },
    h('h1', {}, 'Welcome'),
    h('div', { class: 'panel' },
      h('p', {}, 'Start with a fresh account.'),
      h('button', { class: 'primary', onclick: async () => {
        try { await overfin.createAccount(); start(); } catch (err) { msg.textContent = explain(err); }
      } }, 'Create my account')),
    bring,
    msg));
}

// ---- Profiles ----------------------------------------------------------------

async function profilesPage() {
  $('#top').hidden = true;
  const profiles = await overfin.profiles();
  const msg = h('p', { class: 'error' });
  const pinArea = h('div');

  async function choose(p, pin) {
    msg.textContent = '';
    try {
      await overfin.activateProfile(p.id, pin);
      sessionStorage.setItem('overfin-demo-picked', '1');
      state.me = await overfin.me();
      location.hash = '#/'; // the hashchange shows home
    } catch (err) {
      if (err.type === 'pin-required') return askPIN(p);
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
      profiles.map((p) => h('button', { class: 'profile', onclick: () => (p.hasPin ? askPIN(p) : choose(p)) },
        h('div', { class: 'avatar' }, p.name.slice(0, 1).toUpperCase()), p.name,
        h('div', { class: 'badge' }, [p.kind === 'kids' ? `Kids · ${p.ratingCeiling}` : '', p.hasPin ? '🔒' : ''].filter(Boolean).join(' ')))),
      add),
    pinArea, h('div', { class: 'center' }, msg));
}

async function newProfile() {
  const ratings = await overfin.ratings();
  const name = h('input', { maxlength: '32', required: true });
  const kids = h('input', { type: 'checkbox' });
  const ceiling = h('select', {}, ratings.map((r) => h('option', { value: r }, r)));
  const msg = h('p', { class: 'error' });
  render(h('div', { class: 'center' }, h('h1', {}, 'New profile'),
    h('form', { class: 'stack panel', onsubmit: async (ev) => {
      ev.preventDefault();
      const profile = { name: name.value };
      if (kids.checked) Object.assign(profile, { kind: 'kids', ratingCeiling: ceiling.value });
      try { await overfin.createProfile(profile); profilesPage(); } catch (err) { msg.textContent = explain(err); }
    } },
    h('label', {}, 'Name', name),
    h('label', {}, kids, ' Kids profile, up to rating ', ceiling),
    h('button', { class: 'primary', type: 'submit' }, 'Create'),
    h('button', { type: 'button', onclick: () => profilesPage() }, 'Cancel')),
    msg));
}

// ---- Player --------------------------------------------------------------------

// The player on screen, if any, so leaving the page can stop it.
let player = null;

// Where the next playback starts, when the user just chose (resume, or from
// the start). After a reload there's no choice, and the SDK resumes where the
// profile left off.
let chosenStart;

function play(itemId, positionMs) {
  chosenStart = positionMs;
  location.hash = `#/play/${itemId}`;
}

async function playerPage(itemId) {
  const startMs = chosenStart;
  chosenStart = undefined;
  const video = h('video', { controls: true, autoplay: true, playsinline: true });
  const title = h('div', { class: 'title' });
  const audio = h('select', { 'aria-label': 'Audio', hidden: true });
  const subs = h('select', { 'aria-label': 'Subtitles', hidden: true });
  const skip = h('button', { class: 'skip', hidden: true }, 'Skip intro');
  const box = h('div', { class: 'player' }, video, skip,
    h('div', { class: 'bar' }, h('button', { onclick: () => history.back() }, '← Back'), title, audio, subs));
  render(box);

  function notice(text, ...buttons) {
    box.querySelector('.notice')?.remove();
    box.append(h('div', { class: 'notice' }, h('div', {}, h('p', {}, text),
      h('div', { class: 'actions' }, ...buttons, h('button', { onclick: () => history.back() }, 'Back')))));
  }

  try {
    player = await overfin.play(video, itemId, {
      startMs,
      // Every (re)start: refresh the title and the track menus.
      onStart(pb) {
        box.querySelector('.notice')?.remove();
        title.textContent = pb.item.name + (pb.method !== 'direct' ? ` · ${pb.method}` : '');
        const tracks = pb.audioTracks || [];
        audio.replaceChildren(...tracks.map((t) => h('option', { value: t.id, selected: t.id === pb.audioTrack }, t.label)));
        audio.hidden = tracks.length < 2;
        const subtitleTracks = pb.subtitleTracks || [];
        subs.replaceChildren(h('option', { value: '' }, 'No subtitles'),
          ...subtitleTracks.map((t) => h('option', { value: t.id, selected: t.id === player?.subtitleTrack }, t.label)));
        subs.hidden = subtitleTracks.length === 0;
      },
      onTakenOver(device) {
        notice(`Playback continued on ${device || 'another device'}.`,
          h('button', { class: 'primary', onclick: () => player.continueHere() }, 'Continue here'));
      },
      onError(err) { notice(explain(err)); },
    });
  } catch (err) {
    return notice(explain(err));
  }
  audio.onchange = () => player.setAudioTrack(audio.value);
  subs.onchange = () => player.setSubtitleTrack(subs.value || null);

  // "Skip intro" while inside an intro.
  video.addEventListener('timeupdate', () => {
    const seg = player?.segmentAt();
    skip.hidden = !(seg && seg.kind === 'intro' && player.positionMs < seg.endMs - 1000);
    skip.onclick = () => { if (seg) video.currentTime = seg.endMs / 1000; };
  });
}

async function closePlayer() {
  const p = player;
  player = null;
  if (p) await p.close();
}

// ---- Routing -------------------------------------------------------------------

// Every screen lives in the address, so a reload comes back to it.
async function route() {
  const [path, query] = location.hash.slice(1).split('?');
  const params = new URLSearchParams(query || '');
  const parts = (path || '/').split('/').filter(Boolean);
  await closePlayer();
  try {
    if (parts[0] === 'profiles') return await profilesPage();
    renderHeader();
    switch (parts[0]) {
      case undefined: return await home();
      case 'library': return await libraryPage(parts[1], params);
      case 'item': return await itemPage(parts[1]);
      case 'person': return await personPage(parts[1]);
      case 'account': return accountPage();
      case 'play': return await playerPage(parts[1]);
      default: return notFound();
    }
  } catch (err) {
    if (err instanceof OverfinError && err.status === 404) return notFound();
    console.error(err);
    render(h('div', { class: 'center' }, h('p', { class: 'error' }, explain(err)), h('a', { href: '#/' }, 'Back home')));
  }
}

async function start() {
  try {
    state.me = await overfin.me();
  } catch (err) {
    if (err.type === 'account-setup-required') return setupPage(err.problem.claim);
    if (err.type === 'account-not-allowed') {
      return render(h('div', { class: 'center' }, h('h1', {}, 'No access'), h('p', {}, explain(err))));
    }
    return render(h('p', { class: 'error' }, explain(err)));
  }
  const [libraries, profiles] = await Promise.all([overfin.libraries().catch(() => []), overfin.profiles()]);
  state.libraries = libraries;
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
  await closePlayer();
  sessionStorage.removeItem('overfin-demo-picked');
  const out = await overfin.logout().catch(() => ({}));
  location.href = out.logoutUrl || '/';
});
window.addEventListener('hashchange', route);
window.addEventListener('pagehide', () => { closePlayer(); });
start();
