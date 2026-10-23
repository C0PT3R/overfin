// Overfin control panel. Each view is a function that fetches what it shows
// and draws it into <main>. Text from the server is only ever set with
// textContent (through h()).

const $ = (sel) => document.querySelector(sel);

// h makes an element: attributes (on… are listeners), then children;
// false, null and undefined children are left out.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
    else if (k === 'class') el.className = v;
    else el.setAttribute(k, v === true ? '' : v);
  }
  for (const c of children.flat(Infinity)) {
    if (c === undefined || c === null || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

// fill replaces an element's content, leaving out what's empty.
function fill(el, ...nodes) {
  el.replaceChildren(...nodes.flat(Infinity).filter((n) => n !== undefined && n !== null && n !== false));
}

// svg makes an icon from path data, drawn with the current color.
function icon(paths) {
  const ns = 'http://www.w3.org/2000/svg';
  const s = document.createElementNS(ns, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.8');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  for (const d of paths) {
    const p = document.createElementNS(ns, 'path');
    p.setAttribute('d', d);
    s.append(p);
  }
  return s;
}

const icons = {
  server: ['M4 5h16v6H4z', 'M4 13h16v6H4z', 'M8 8h.01', 'M8 16h.01'],
  sliders: ['M4 6h10', 'M18 6h2', 'M4 12h4', 'M12 12h8', 'M4 18h12', 'M20 18h0', 'M16 4v4', 'M10 10v4', 'M18 16v4'],
  people: ['M16 19v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1', 'M9.5 10a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7z', 'M21 19v-1a4 4 0 0 0-3-3.87', 'M16 3.13a3.5 3.5 0 0 1 0 6.75'],
  layers: ['M12 3l9 5-9 5-9-5 9-5z', 'M3 13l9 5 9-5'],
  mail: ['M4 6h16v12H4z', 'M4 7l8 6 8-6'],
  move: ['M5 12h14', 'M13 6l6 6-6 6'],
  check: ['M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z', 'M20 20l-4-4'],
  key: ['M15 7a4 4 0 1 1-3.87 5H4v3h3v3h3v-3h1.13', 'M15.5 8.5h.01'],
  puzzle: ['M10 4h4v3a2 2 0 1 0 4 0V4h2v6h-3a2 2 0 1 0 0 4h3v6h-6v-3a2 2 0 1 0-4 0v3H4v-6h3a2 2 0 1 0 0-4H4V4h6z'],
  folder: ['M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z'],
};

async function api(method, path, body) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (res.status === 401) {
    location.href = '/admin/login?return=/admin/';
    throw new Error('signing in');
  }
  if (!res.ok) {
    const err = new Error(data.detail || data.error || data.type || `HTTP ${res.status}`);
    err.type = data.type;
    throw err;
  }
  return data;
}

// ---- Building blocks -----------------------------------------------------------

// toast shows a short message in the corner for a few seconds.
function toast(text, error = false) {
  const t = h('div', { class: error ? 'toast error' : 'toast', role: 'status' }, text);
  $('#toasts').append(t);
  setTimeout(() => t.remove(), error ? 7000 : 3500);
}

// busy runs an action with its button disabled, and tells any failure.
async function busy(button, action, fail = (err) => toast(err.message, true)) {
  if (button) button.disabled = true;
  try {
    return await action();
  } catch (err) {
    if (err.message !== 'signing in') fail(err);
    return undefined;
  } finally {
    if (button) button.disabled = false;
  }
}

function pageHead(title, description, ...actions) {
  return h('div', { class: 'page-head' },
    h('div', {}, h('h1', {}, title), description && h('p', {}, description)),
    actions.length > 0 && h('div', { class: 'actions' }, actions));
}

function badge(text, kind = '') {
  return h('span', { class: `badge ${kind}` }, text);
}

// card is a section with a head (title, badge, description), a body and a
// foot for its status and actions.
function card({ title, badge: b, description }, body, foot) {
  return h('section', {},
    h('div', { class: 'card-head' }, h('div', {}, h('h2', {}, title), description && h('p', {}, description)), b),
    body, foot && h('div', { class: 'card-foot' }, foot));
}

function field(label, input, hint, wide = false) {
  return h('label', { class: wide ? 'field full' : 'field' }, h('span', {}, label), input, hint && h('span', { class: 'hint' }, hint));
}

function setting(label, hint, control, sub = false) {
  return h('div', { class: sub ? 'setting sub' : 'setting' },
    h('div', {}, h('div', { class: 'label' }, label), hint && h('div', { class: 'hint' }, hint)), control);
}

// status is a line that says where something stands, or what went wrong.
function status(text) {
  const el = h('span', { class: 'status' }, text);
  el.set = (t, kind = '') => { el.className = `status ${kind}`; el.textContent = t; };
  return el;
}

// avatar is someone's initials on a color of their own (one of eight
// classes: the panel's CSP allows no inline style).
function avatar(name, big = false) {
  const text = (name || '?').trim();
  const initials = text.includes('@') ? text[0] : text.split(/\s+/).slice(0, 2).map((w) => w[0]).join('');
  let n = 0;
  for (const c of text) n = (n * 31 + c.charCodeAt(0)) >>> 0;
  return h('span', { class: `avatar c${n % 8}${big ? ' big' : ''}`, 'aria-hidden': 'true' }, initials.toUpperCase());
}

// secret shows a value to copy once: a key, a link.
function secret(intro, value, after) {
  const input = h('input', { value, readonly: true, class: 'mono', onfocus: (e) => e.target.select() });
  const copy = h('button', { type: 'button', onclick: () => navigator.clipboard?.writeText(value).then(() => toast('Copied.'), () => input.select()) }, 'Copy');
  setTimeout(() => input.select(), 0);
  return h('div', { class: 'notice secret' }, h('div', {}, intro), h('div', { class: 'row' }, input, copy), after && h('div', { class: 'hint' }, after));
}

const when = (iso) => (iso ? new Date(iso).toLocaleString() : '');
const day = (iso) => (iso ? new Date(iso).toLocaleDateString() : '');

// ago says how long ago, roughly: "5 min ago", "3 h ago", "2 d ago".
function ago(iso) {
  if (!iso) return '';
  const min = Math.round((Date.now() - new Date(iso)) / 60000);
  if (min < 1) return 'just now';
  if (min < 60) return `${min} min ago`;
  if (min < 48 * 60) return `${Math.round(min / 60)} h ago`;
  return `${Math.round(min / 1440)} d ago`;
}

// toLocalInput turns an RFC 3339 time into a datetime-local value.
function toLocalInput(iso) {
  if (!iso) return '';
  const d = new Date(iso);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const plural = (n, one, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// ---- Libraries -----------------------------------------------------------------

const kindNames = { films: 'Films', series: 'Series' };

// librariesView: the libraries of Overfin's own Jellyfin, with their
// folders as Jellyfin sees them, and a scan on demand.
async function librariesView() {
  const { items } = await api('GET', '/admin/api/libraries');
  const reload = () => librariesView();
  const adding = h('div', {});
  const add = h('button', { class: 'primary', type: 'button', onclick: () => {
    fill(adding, libraryForm(null, reload, () => fill(adding)));
    adding.querySelector('input')?.focus();
  } }, 'Add a library');
  const scan = h('button', { type: 'button', onclick: () => busy(scan, async () => {
    await api('POST', '/admin/api/libraries/scan', {});
    toast('Jellyfin is reading every folder again.');
    setTimeout(reload, 1500);
  }) }, 'Scan now');
  show(pageHead('Libraries', 'Where your films and series are, folder by folder, as Jellyfin sees them. New files show up by themselves; a scan reads everything again.', scan, add),
    adding,
    items.length
      ? h('div', { class: 'cards' }, items.map((lib) => libraryCard(lib, reload)))
      : h('p', { class: 'notice' }, 'No library yet. Add one with the folders where your films or series are.'));
  // While Jellyfin scans, its progress, unless a form is open.
  if (items.some((l) => l.scanning)) {
    setTimeout(() => { if (location.hash === '#/libraries' && !$('#main form')) reload(); }, 3000);
  }
}

function libraryCard(lib, reload) {
  const body = h('div', {});
  const view = () => fill(body,
    h('ul', { class: 'list' }, lib.folders.map((f) => h('li', {}, icon(icons.folder), h('div', { class: 'mono' }, f)))),
    h('div', { class: 'actions-row' }, edit, remove));
  const edit = h('button', { type: 'button', class: 'small', onclick: () => fill(body, libraryForm(lib, reload, view)) }, 'Change');
  const remove = h('button', { type: 'button', class: 'small danger', onclick: () => {
    if (!confirm(`Remove the library ${lib.name}? Its files stay where they are; Overfin and its apps stop showing them.`)) return;
    busy(remove, async () => {
      await api('DELETE', `/admin/api/libraries/${encodeURIComponent(lib.name)}`);
      toast(`${lib.name} removed.`);
      reload();
    });
  } }, 'Remove');
  view();
  const b = lib.scanning ? badge(`Scanning ${Math.round(lib.progress)}%`, 'info') : badge(kindNames[lib.kind] || lib.kind, 'plain');
  return card({ title: lib.name, badge: b }, body);
}

// libraryForm adds a library (lib null) or changes one: its name, its
// kind when it's new, and its folders, picked as Jellyfin sees them.
function libraryForm(lib, done, cancel) {
  const name = h('input', { value: lib?.name || '', autocomplete: 'off', placeholder: 'Films' });
  const kind = h('select', {}, h('option', { value: 'films' }, 'Films'), h('option', { value: 'series' }, 'Series'));
  const folders = [...(lib?.folders || [])];
  const list = h('ul', { class: 'list' });
  const picker = h('div', {});
  const draw = () => fill(list, folders.length ? folders.map((f, i) => h('li', {}, icon(icons.folder), h('div', { class: 'mono' }, f),
    h('button', { type: 'button', class: 'small ghost', onclick: () => { folders.splice(i, 1); draw(); } }, 'Remove')))
    : h('li', {}, h('div', { class: 'hint' }, 'No folder yet.')));
  const addFolder = h('button', { type: 'button', class: 'small', onclick: () => fill(picker, folderPicker((f) => {
    if (!folders.includes(f)) folders.push(f);
    draw();
    fill(picker);
  }, () => fill(picker))) }, 'Add a folder');
  const save = h('button', { class: 'primary', type: 'submit' }, lib ? 'Save' : 'Add');
  const submit = (e) => {
    e.preventDefault();
    busy(save, async () => {
      if (lib) {
        await api('PATCH', `/admin/api/libraries/${encodeURIComponent(lib.name)}`, { name: name.value, folders });
        toast(`${name.value} saved.`);
      } else {
        await api('POST', '/admin/api/libraries', { name: name.value, kind: kind.value, folders });
        toast(`${name.value} added; Jellyfin is reading it.`);
      }
      done();
    });
  };
  draw();
  const form = h('form', { onsubmit: submit },
    h('div', { class: 'fields' }, field('Name', name), !lib && field('Holds', kind)),
    h('h3', {}, 'Folders'), list, h('div', { class: 'actions-row' }, addFolder), picker,
    h('div', { class: 'actions-row' }, h('button', { type: 'button', class: 'ghost', onclick: cancel }, 'Cancel'), save));
  return lib ? form : card({ title: 'Add a library', description: 'Films and series go in libraries of their own. Folders are as Jellyfin sees them: in a container, its paths, not the host\'s.' }, form);
}

// folderPicker browses folders as Jellyfin sees them, and picks one.
function folderPicker(pick, close) {
  const box = h('div', { class: 'box' });
  const typed = h('input', { class: 'mono', placeholder: '/media/films', autocomplete: 'off', spellcheck: 'false' });
  const open = async (path) => {
    try {
      const d = await api('GET', `/admin/api/folders?path=${encodeURIComponent(path)}`);
      typed.value = d.path;
      fill(box,
        h('div', { class: 'row' }, typed,
          h('button', { type: 'button', onclick: () => open(typed.value) }, 'Go'),
          h('button', { type: 'button', class: 'primary', disabled: !d.path, onclick: () => pick(d.path) }, 'Choose this folder'),
          h('button', { type: 'button', class: 'ghost', onclick: close }, 'Cancel')),
        h('ul', { class: 'list' },
          d.parent !== '' && h('li', {}, h('a', { href: '#', onclick: (e) => { e.preventDefault(); open(d.parent); } }, '↑ Up')),
          d.folders.map((f) => h('li', {}, icon(icons.folder),
            h('a', { href: '#', class: 'mono', onclick: (e) => { e.preventDefault(); open(f); } }, f.split('/').pop() || f))),
          !d.folders.length && h('li', {}, h('div', { class: 'hint' }, 'No folders in here.'))));
    } catch (err) {
      toast(err.message, true);
    }
  };
  typed.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); open(typed.value); } });
  open('');
  return box;
}

// ---- Server --------------------------------------------------------------------

// serverView: what Overfin connects to, each part tested before it's saved.
async function serverView() {
  const [jellyfin, mail, catalog, seerr, ratings] = await Promise.all([
    api('GET', '/admin/api/jellyfin').catch(() => null),
    api('GET', '/admin/api/mail').catch(() => null),
    api('GET', '/admin/api/catalog').catch(() => null),
    api('GET', '/admin/api/seerr').catch(() => null),
    api('GET', '/admin/api/ratings').catch(() => null),
  ]);
  show(pageHead('Server', 'What Overfin connects to. Every change is tested before it\'s saved, and applies at once.'),
    h('div', { class: 'cards' }, jellyfinCard(jellyfin), catalogCard(catalog), mailCard(mail), seerrCard(seerr), tmdbCard(ratings)));
}

function inFileCard(title, text) {
  return card({ title, badge: badge('Configuration file', 'info') },
    h('div', { class: 'box' }, h('div', {}, text), h('div', { class: 'hint' }, "Set in Overfin's configuration file: change it there.")));
}

function jellyfinCard(j) {
  if (!j) return null;
  const description = 'Overfin\'s own Jellyfin, where your media is read. Overfin is the only one that talks to it, with a key it made itself.';
  if (j.inFile) {
    return card({ title: 'Jellyfin', badge: badge('Connected', 'ok'), description },
      [h('div', { class: 'box' }, h('div', {}, j.url), h('div', { class: 'hint' }, "Set in Overfin's configuration file: change it there.")),
        jellyfinAccess(j.access)]);
  }
  const url = h('input', { value: j.url, autocomplete: 'off', spellcheck: 'false' });
  const st = status('Connected.');
  const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
    st.set('Testing…');
    const out = await api('PUT', '/admin/api/jellyfin', { url: url.value });
    url.value = out.url;
    st.set(out.summary, 'ok');
    toast('Jellyfin saved.');
  }, (err) => st.set(err.message, 'error')) }, 'Test and save');
  return card({ title: 'Jellyfin', badge: badge('Connected', 'ok'), description },
    [h('div', { class: 'fields' },
      field('Address', url, 'As Overfin reaches it; a new address for the same server. Not localhost: in a container, that\'s Overfin itself.', true)),
    jellyfinAccess(j.access)],
    [st, save]);
}

// jellyfinAccess opens or closes access to Jellyfin itself: closed, nobody
// can sign in there; opened, Overfin's administrator account gets a new
// password, shown here once.
function jellyfinAccess(a) {
  const box = h('div', {});
  const draw = (open, since) => {
    const button = h('button', { type: 'button', class: open ? 'small' : 'small danger', onclick: () => {
      if (!open && !confirm('Open access to Jellyfin itself? Changes made there may break Overfin, and Overfin sets the settings it needs back when it starts.')) return;
      busy(button, async () => {
        const out = await api('PUT', '/admin/api/jellyfin/access', { open: !open });
        if (open) {
          toast('Access to Jellyfin closed.');
          draw(false);
          return;
        }
        draw(true, new Date().toISOString());
        box.append(secret(`Sign in to Jellyfin as ${out.username} with this password:`, out.password,
          'Shown once, never kept. Close access when you\'re done: that ends every sign-in there.'));
      });
    } }, open ? 'Close access' : 'Open access');
    fill(box, h('h3', {}, 'Access to Jellyfin itself'),
      open
        ? h('p', { class: 'notice warn' }, `Open since ${when(since)}. Changes made in Jellyfin may break Overfin, and the settings it needs are set back when it starts.`)
        : h('p', { class: 'hint' }, 'Closed: nobody can sign in to Jellyfin itself, and Overfin doesn\'t need anyone to. Open it only to look inside, for a while.'),
      h('div', { class: 'actions-row' }, button));
  };
  draw(!!a?.open, a?.since);
  return box;
}

function catalogCard(c) {
  if (!c) return null;
  const t = c.titles || {};
  const n = (v) => (v || 0).toLocaleString();
  const first = !c.fullAt || c.fullAt.startsWith('0001');
  const u = c.unmatched || {};
  const parts = [
    [u.movie, 'films', 'without a TMDB ID'], [u.series, 'series', 'without a TMDB ID'],
    [u.season, 'seasons', 'without their number'], [u.episode, 'episodes', 'without their season or episode number'],
  ].filter(([k]) => k > 0).map(([k, label, why]) => `${k} ${label} ${why}`);
  return card({
    title: 'Library index',
    badge: c.error ? badge('Last read failed', 'err') : first ? badge('First read', 'warn') : badge('Up to date', 'ok'),
    description: first ? 'The library is being read for the first time.' : `Read in full ${ago(c.fullAt)}; changes read ${ago(c.changesAt)}.`,
  },
  [
    h('div', { class: 'stats' }, [['movie', 'Films'], ['series', 'Series'], ['season', 'Seasons'], ['episode', 'Episodes']]
      .map(([k, label]) => h('div', { class: 'stat' }, h('b', {}, n(t[k])), h('span', {}, label)))),
    parts.length > 0 && h('p', { class: 'hint' }, `Can't be matched yet: ${parts.join('; ')}. Refreshing their metadata in Jellyfin usually fixes it; Identification lists them.`),
    c.error && h('p', { class: 'error' }, c.error),
  ]);
}

function mailCard(m) {
  if (!m) return null;
  if (m.inFile) return inFileCard('Email', `${m.host}:${m.port}, from ${m.from}`);
  const host = h('input', { value: m.host || '', autocomplete: 'off', spellcheck: 'false', placeholder: 'smtp.example.org' });
  const tls = h('select', {}, [['starttls', 'STARTTLS'], ['tls', 'TLS'], ['none', 'None']]
    .map(([v, t]) => h('option', { value: v, selected: v === (m.tls || 'starttls') }, t)));
  const port = h('input', { type: 'number', min: '1', max: '65535', value: m.port || '', placeholder: 'The usual one' });
  const from = h('input', { value: m.from || '', autocomplete: 'off', placeholder: 'Media <media@example.org>' });
  const username = h('input', { value: m.username || '', autocomplete: 'off', spellcheck: 'false' });
  const password = h('input', { type: 'password', autocomplete: 'new-password', placeholder: m.hasPassword ? 'Unchanged' : '' });
  const on = Boolean(m.host);
  const st = status(on ? 'People can get sign-in codes by email.' : 'People sign in with a password, or a sign-in link you hand them.');
  const send = (body, button) => busy(button, async () => {
    st.set(body.host ? 'Sending you a test email…' : 'Turning email off…');
    const out = await api('PUT', '/admin/api/mail', body);
    st.set(out.summary, 'ok');
    toast(body.host ? 'Email saved.' : 'Email turned off.');
    route();
  }, (err) => st.set(err.message, 'error'));
  const save = h('button', { class: 'primary', type: 'button', onclick: () => send({ host: host.value, port: Number(port.value) || 0, tls: tls.value,
    from: from.value, username: username.value, password: password.value }, save) }, 'Send a test and save');
  const off = on && h('button', { type: 'button', onclick: () => {
    if (confirm('Turn email off? People without a password will need a sign-in link from you.')) {
      send({ host: '', port: 0, tls: '', from: '', username: '', password: '' }, off);
    }
  } }, 'Turn off');
  return card({ title: 'Email', badge: on ? badge('On', 'ok') : badge('Off'),
    description: 'For sign-in codes. Your email provider gives these settings; a test email goes to you before they\'re saved.' },
  h('div', { class: 'fields' },
    field('SMTP server', host, null, true),
    field('Encryption', tls, 'STARTTLS goes with port 587, TLS with 465. None: a relay on your network.'),
    field('Port', port),
    field('Sender', from, 'The address emails come from.', true),
    field('Username', username, 'Often the sender\'s address.'),
    field('Password', password, m.hasPassword ? 'Empty keeps the current one.' : null)),
  [st, off, save]);
}

function seerrCard(sr) {
  if (!sr) return null;
  const url = h('input', { value: sr.url, autocomplete: 'off', spellcheck: 'false', placeholder: 'http://192.168.1.10:5055' });
  const key = h('input', { type: 'password', autocomplete: 'off', placeholder: sr.url ? 'Unchanged' : '' });
  const st = status(sr.url ? 'Requests go to Seerr.' : 'Without Seerr, nobody can request titles.');
  const send = (body, button) => busy(button, async () => {
    st.set('Testing…');
    const out = await api('PUT', '/admin/api/seerr', body);
    st.set(out.summary, 'ok');
    toast(out.url ? 'Seerr saved.' : 'Seerr disconnected.');
    route();
  }, (err) => st.set(err.message, 'error'));
  const save = h('button', { class: 'primary', type: 'button', onclick: () => send({ url: url.value, key: key.value }, save) }, 'Test and save');
  const off = sr.url && h('button', { type: 'button', onclick: () => {
    if (confirm('Disconnect Seerr? Requests stop until it\'s connected again.')) send({ url: '', key: '' }, off);
  } }, 'Disconnect');
  return card({ title: 'Seerr', badge: sr.url ? badge('Connected', 'ok') : badge('Not connected'),
    description: 'Optional: where requests go. Keep it off the internet; only Overfin needs to reach it.' },
  h('div', { class: 'fields' },
    field('Address', url, 'As Overfin reaches it.', true),
    field('API key', key, 'In Seerr\'s settings, under General.', true)),
  [st, off, save]);
}

// TMDB: the ratings country kids profiles are held to, the language, and
// which titles not in the libraries are shown.
function tmdbCard(rt) {
  if (!rt) return null;
  let names = null;
  try { names = new Intl.DisplayNames([navigator.language, 'en'], { type: 'region' }); } catch { names = null; }
  const countries = rt.countries.length ? rt.countries : [rt.country];
  const country = h('select', {}, countries
    .map((cc) => [cc, `${(names && names.of(cc)) || cc} (${cc})`])
    .sort((a, b) => a[1].localeCompare(b[1]))
    .map(([cc, label]) => h('option', { value: cc }, label)));
  country.value = rt.country;
  let langNames = null;
  try { langNames = new Intl.DisplayNames([navigator.language, 'en'], { type: 'language' }); } catch { langNames = null; }
  const languages = ['ar', 'cs', 'da', 'de', 'el', 'en', 'es', 'fi', 'fr', 'he', 'hi', 'hu', 'id', 'it', 'ja', 'ko', 'nl', 'no',
    'pl', 'pt', 'ro', 'ru', 'sv', 'th', 'tr', 'uk', 'vi', 'zh'];
  if (!languages.includes(rt.language)) languages.push(rt.language);
  const lang = h('select', {}, languages.map((l) => h('option', { value: l }, `${(langNames && langNames.of(l)) || l} (${l})`)));
  lang.value = rt.language;
  const shown = h('select', {}, [['all', 'All, announced ones too'], ['released', 'Released, in theatres too'],
    ['home', 'Watchable at home']].map(([v, t]) => h('option', { value: v }, t)));
  shown.value = rt.shown;
  const describe = (r) => {
    if (!r.source) return ['TMDB can\'t be reached: no title can be rated, so kids profiles see nothing.', 'error'];
    if (!r.countries.length) return ['Fetching the rating scales from TMDB… Kids profiles see nothing until titles are rated.', ''];
    return [`${r.fetched.toLocaleString()} of ${r.total.toLocaleString()} films and series rated.`, ''];
  };
  const st = status('');
  st.set(...describe(rt));
  const save = h('button', { class: 'primary', type: 'button', disabled: !rt.countries.length, onclick: () => busy(save, async () => {
    if (country.value === rt.country && lang.value === rt.language && shown.value === rt.shown) return;
    if (country.value !== rt.country &&
      !confirm('Every kids profile will need new limits from its account\'s owner, and sees nothing until then. Change the country?')) {
      country.value = rt.country;
      return;
    }
    const out = await api('PUT', '/admin/api/ratings', { country: country.value, language: lang.value, shown: shown.value });
    Object.assign(rt, { country: out.country, language: out.language, shown: out.shown });
    st.set(...describe(out));
    toast('TMDB settings saved.');
  }, (err) => st.set(err.message, 'error')) }, 'Save');
  return card({ title: 'TMDB', badge: rt.source ? badge('Reachable', 'ok') : badge('Unreachable', 'err'),
    description: 'Where titles, artwork and ratings come from.' },
  [
    setting('Ratings country', 'Kids profiles\' limits use this country\'s ratings. A title with none there is hidden from them.', country),
    setting('Language', 'Of titles, descriptions and the text on artwork, when TMDB has them in it.', lang),
    setting('Titles shown', 'Of those no library has, in discovery and requests. A title a library has is always shown.', shown),
  ],
  [st, save]);
}

// ---- Features and limits -------------------------------------------------------

const DRAFT = 'overfin-panel-draft';

async function featuresView() {
  const state = await api('GET', '/admin/api/features');
  let draft = null;
  try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
  // A draft fills in over the current settings: one kept from an older
  // version may lack sections, or have some that are gone.
  const start = structuredClone({ ...state.features,
    ...Object.fromEntries(Object.entries(draft || {}).filter(([k]) => k in state.features)) });

  const fields = [];
  const bind = (path, get, set, input, value) => {
    fields.push({ path, get, set, input, value });
    input.addEventListener(input.type === 'checkbox' ? 'change' : 'input', changed);
    return input;
  };
  const num = (label, hint, path, min, max) => setting(label, hint,
    bind(path, (el) => Number(el.value), (el, v) => { el.value = v; }, h('input', { type: 'number', min: String(min), max: max && String(max) })));
  const choice = (label, hint, path, options, sub) => setting(label, hint,
    bind(path, (el) => el.value, (el, v) => { el.value = v; }, h('select', {}, options.map(([v, t]) => h('option', { value: v }, t)))), sub);
  const toggle = (label, hint, path, value, sub) => setting(label, hint,
    bind(path, (el) => el.checked, (el, list) => { el.checked = (list || []).includes(value); }, h('input', { type: 'checkbox', 'aria-label': label }), value), sub);
  const onOff = (label, hint, path) => setting(label, hint,
    bind(path, (el) => (el.checked ? 'on' : 'off'), (el, v) => { el.checked = v === 'on'; }, h('input', { type: 'checkbox', 'aria-label': label })), true);

  // Each feature: its switch, and under it its own settings, dimmed while
  // it's off.
  const features = state.plugins.map((p) => {
    const subs = (p.settings || []).map((s) => {
      const path = `modules.${p.name}.${s.key}`;
      return s.choices ? choice(s.label, s.hint, path, s.choices.map((c) => [c.value, c.label]), true) : onOff(s.label, s.hint, path);
    });
    const main = toggle(featureName(p.name), [p.description, p.needs && `Needs ${p.needs.map(featureName).join(', ')}.`].filter(Boolean).join(' '),
      'plugins.enable', p.name);
    const sw = main.querySelector('input');
    const dim = () => subs.forEach((s) => s.classList.toggle('off', !sw.checked));
    sw.addEventListener('change', dim);
    return { main, subs, dim };
  });

  const at = (obj, path) => path.split('.').reduce((o, k) => o?.[k], obj);
  const load = (f) => {
    for (const fl of fields) fl.set(fl.input, at(f, fl.path));
    features.forEach((x) => x.dim());
  };
  const collect = () => {
    const out = structuredClone(state.features);
    for (const fl of fields) {
      const keys = fl.path.split('.');
      const parent = keys.slice(0, -1).reduce((o, k) => (o[k] ??= {}), out);
      const key = keys.at(-1);
      if (fl.value !== undefined) {
        const list = new Set(parent[key] || []);
        if (fl.get(fl.input)) list.add(fl.value); else list.delete(fl.value);
        parent[key] = [...list].sort();
      } else {
        parent[key] = fl.get(fl.input);
      }
    }
    return out;
  };
  // What's saved, as the form reads it back: unsaved changes are any
  // difference from it.
  let baseline = '';
  const settle = () => {
    load(state.features);
    baseline = JSON.stringify(collect());
  };

  const bar = h('div', { class: 'savebar', hidden: true });
  const message = h('span', { class: 'grow' }, 'You have unsaved changes.');
  const discard = h('button', { type: 'button', class: 'ghost', onclick: () => {
    sessionStorage.removeItem(DRAFT);
    settle();
    changed();
  } }, 'Discard');
  const save = h('button', { type: 'button', class: 'primary', onclick: () => busy(save, async () => {
    const wanted = collect();
    try {
      const out = await api('PUT', '/admin/api/features', wanted);
      sessionStorage.removeItem(DRAFT);
      state.features = out.features;
      settle();
      changed();
      toast('Saved and applied.');
    } catch (err) {
      if (err.type === 'step-up-required') {
        // Changes need a recent sign-in: keep them, sign in, come back.
        sessionStorage.setItem(DRAFT, JSON.stringify(wanted));
        location.href = '/admin/login?return=/admin/%23/features';
        return;
      }
      throw err;
    }
  }) }, 'Save changes');
  fill(bar, message, discard, save);
  function changed() {
    bar.hidden = JSON.stringify(collect()) === baseline;
  }

  show(
    pageHead('Features & limits', 'What this server offers, and how far it goes. Changes apply once saved.'),
    draft && h('div', { class: 'notice warn' }, 'Your unsaved changes are back: save them to apply them.'),
    card({ title: 'Features', description: 'Switched on and off for everyone at once.' }, features.map((x) => [x.main, x.subs])),
    h('div', { class: 'cards' },
      card({ title: 'Streams' }, [
        choice('One stream too many', 'When an account starts more streams than its template allows.', 'limits.onLimit',
          [['replace-oldest', 'Replace the oldest'], ['refuse', 'Refuse the new one']]),
        num('Transcodes on the server', 'Re-encoded video only, not repackaging. 0: no limit.', 'limits.maxTranscodesTotal', 0),
        num('Bitrate cap (kbps)', '0: no cap.', 'limits.maxBitrateKbps', 0),
      ]),
      card({ title: 'Accounts' }, [
        choice('When an account is deleted', 'What happens to its watch history.', 'accounts.onDelete',
          [['delete', 'Delete it too'], ['disable', 'Keep it, unusable']]),
        num('Wrong PINs before a lock', 'From 3 to 10.', 'limits.pinAttempts', 3, 10),
      ])),
    card({ title: 'Privacy', description: 'Details hidden from everyone unless switched on here.' },
      state.exposableFields.map((name) => toggle(privacyName(name), null, 'privacy.expose', name))),
    bar);
  settle();
  load(start);
  changed();
}

const featureNames = { library: 'Library', playback: 'Playback', watchlist: 'Watchlist', invites: 'Invitations', migration: 'Migration' };
const featureName = (n) => featureNames[n] || n.charAt(0).toUpperCase() + n.slice(1);
const privacyNames = {
  'version.names': 'Version names (from file names)', 'photo.takenAt': 'When photos were taken', 'photo.location': 'Where photos were taken',
  'photo.camera': 'Which camera took photos', 'item.tags': 'Tags',
};
const privacyName = (n) => privacyNames[n] || n;

// ---- People --------------------------------------------------------------------

const statusText = { active: 'Active', disabled: 'Disabled', expired: 'Expired' };
const statusKind = { active: 'ok', disabled: 'err', expired: 'warn' };
const lastActive = (a) => a.sessions.map((s) => s.lastActiveAt).sort().at(-1) || '';
// nameOf is the best name for an account: the identity provider's, then the
// operator's label, then the owner profile's name.
const nameOf = (a) => a.person?.name || a.person?.username || a.label || a.profiles.find((p) => p.owner)?.name || 'Unnamed';
const subline = (a) => [a.person?.username && a.person.name ? a.person.username : '', a.person?.email && a.person.email !== nameOf(a) ? a.person.email : '']
  .filter(Boolean).join(' · ');

async function peopleView(selected = '') {
  const [{ items, directory }, { items: templates }, people] = await Promise.all([
    api('GET', '/admin/api/accounts'), api('GET', '/admin/api/templates'),
    // Only with managed sign-in: who may sign in, by email.
    api('GET', '/admin/api/people').then((r) => r.items).catch(() => null)]);
  $('#main').classList.add('wide');
  const reload = (id = selected) => peopleView(id);
  const search = h('input', { type: 'search', placeholder: 'Search by name, email or profile', 'aria-label': 'Search' });
  const filter = h('select', { 'aria-label': 'Status' }, h('option', { value: '' }, 'Every status'),
    ...Object.entries(statusText).map(([v, t]) => h('option', { value: v }, t)));
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('div', { class: 'placeholder' }, 'Select someone to see their account.'));

  const select = (a) => {
    selected = a.id;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === a.id);
    fill(detail, accountDetail(a, templates, reload, people));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((a) => (!filter.value || a.status === filter.value) &&
      (!q || [a.label, a.person?.name, a.person?.username, a.person?.email, a.subject, a.id, ...a.profiles.map((p) => p.name)]
        .some((v) => (v || '').toLowerCase().includes(q))));
    fill(tbody, shown.map((a) => {
      const kids = a.profiles.filter((p) => p.kind === 'kids').length;
      return h('tr', { 'data-id': a.id, onclick: () => select(a) },
        h('td', {}, h('div', { class: 'who' }, avatar(nameOf(a)), h('div', {}, h('div', { class: 'name' }, nameOf(a)),
          h('div', { class: 'hint' }, subline(a) || (a.person ? '' : 'name unknown'))))),
        h('td', {}, badge(statusText[a.status] || a.status, statusKind[a.status]),
          a.person && !a.person.active && h('div', { class: 'hint' }, 'off at the identity provider')),
        h('td', { class: 'hide-medium' }, a.template.name),
        h('td', { class: 'num' }, String(a.profiles.length), kids > 0 && h('span', { class: 'hint' }, ` · ${kids} kids`)),
        h('td', { class: 'hint hide-narrow' }, ago(lastActive(a)) || 'never'));
    }));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '5', class: 'empty' }, items.length ? 'Nobody matches.' : 'No accounts yet.')));
    const current = items.find((a) => a.id === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);

  const notice = !people && {
    none: 'Names come from the identity provider through a connector (see [connector] in the configuration); until then, labels tell accounts apart.',
    unreachable: 'The identity provider\'s connector didn\'t answer, so names are missing for now. The log says why.',
  }[directory];
  const waiting = people ? people.filter((x) => !x.accountId) : [];
  const adding = h('div', {});
  const addButton = people && h('button', { class: 'primary', type: 'button', onclick: () => {
    fill(adding, addPerson(() => reload(), () => fill(adding)));
    adding.querySelector('input')?.focus();
  } }, 'Add a person');

  show(
    pageHead('People', people
      ? 'Everyone who can sign in, and their accounts. People sign in with their email: a code sent there, a password, or a link you hand them.'
      : 'Everyone with an account here.', addButton),
    adding,
    h('div', { class: 'split' },
      h('div', {},
        h('div', { class: 'toolbar' }, search, filter),
        notice && h('p', { class: 'notice' }, notice),
        h('div', { class: 'table-wrap' }, h('table', {},
          h('thead', {}, h('tr', {},
            h('th', {}, `Accounts (${items.length})`), h('th', {}, 'Status'), h('th', { class: 'hide-medium' }, 'Template'),
            h('th', {}, 'Profiles'), h('th', { class: 'hide-narrow' }, 'Last active'))),
          tbody)),
        waiting.length > 0 && card({ title: `No account yet (${waiting.length})`,
          description: 'They can sign in; their account is made when they first open the site.' },
        h('ul', { class: 'list' }, waiting.map((x) => h('li', {}, avatar(x.email), h('div', {}, personControls(x, () => reload())))))),
      ),
      detail));
  draw();
}

// addPerson is the form to let someone new sign in, by email.
function addPerson(reload, close) {
  const email = h('input', { type: 'email', placeholder: 'name@example.org', autocomplete: 'off' });
  const admin = h('input', { type: 'checkbox' });
  const add = h('button', { class: 'primary', type: 'submit' }, 'Add');
  const submit = (e) => {
    e.preventDefault();
    busy(add, async () => {
      await api('POST', '/admin/api/people', { email: email.value, admin: admin.checked });
      toast(`${email.value} can sign in now.`);
      reload();
    });
  };
  return card({ title: 'Add a person', description: 'They sign in with this email: a code sent there, or a sign-in link you make for them below.' },
    h('form', { class: 'toolbar', onsubmit: submit },
      field('Email', email),
      h('label', { class: 'inline' }, admin, 'Admin'),
      h('button', { type: 'button', class: 'ghost', onclick: close }, 'Cancel'), add));
}

// personControls are what an admin can do about someone who signs in by
// email: a sign-in link to hand over, the admin mark, and, before they have
// an account, removing them.
function personControls(x, reload) {
  const linkBox = h('div', {});
  const linkButton = h('button', { type: 'button', class: 'small', onclick: () => busy(linkButton, async () => {
    const { link } = await api('POST', `/admin/api/people/${x.id}/link`, {});
    fill(linkBox, secret('A sign-in link for ' + x.email + ':', location.origin + link, 'Works once, for a week, in any browser. Hand it over privately.'));
  }) }, 'Sign-in link');
  const adminButton = !x.you && h('button', { type: 'button', class: 'small', onclick: () => busy(adminButton, async () => {
    await api('PATCH', `/admin/api/people/${x.id}`, { admin: !x.admin });
    toast(x.admin ? `${x.email} is no longer an admin.` : `${x.email} is an admin now.`);
    reload();
  }) }, x.admin ? 'Remove admin' : 'Make admin');
  const removeButton = !x.accountId && !x.you && h('button', { type: 'button', class: 'small danger', onclick: () => {
    if (!confirm(`Remove ${x.email}? They can't sign in any more.`)) return;
    busy(removeButton, async () => {
      await api('DELETE', `/admin/api/people/${x.id}`);
      toast(`${x.email} removed.`);
      reload();
    });
  } }, 'Remove');
  return [
    h('div', { class: 'name' }, x.email, ' ', x.admin && badge('Admin', 'info'), ' ', x.you && badge('You', 'plain'), ' ', x.disabled && badge('Disabled', 'err')),
    h('div', { class: 'hint' }, x.hasPassword ? 'Has a password' : 'No password: signs in with codes or links'),
    h('div', { class: 'actions-row' }, linkButton, adminButton, removeButton),
    linkBox,
  ];
}

// accountDetail is the selected account: who, how they sign in, access,
// profiles, devices, and the destructive actions, set apart.
function accountDetail(a, templates, reload, people) {
  const signsIn = people?.find((x) => x.accountId === a.id);
  const patch = (button, change, done) => busy(button, async () => {
    await api('PATCH', `/admin/api/accounts/${a.id}`, change);
    toast(done);
    reload(a.id);
  });
  const label = h('input', { value: a.label || '', maxlength: '64', placeholder: 'e.g. Bob' });
  const expires = h('input', { type: 'datetime-local', value: toLocalInput(a.expiresAt) });
  const template = h('select', {}, templates.map((t) => h('option', { value: t.id, selected: t.id === a.template.id },
    `${t.name} · ${plural(t.maxStreams, 'stream')}, ${plural(t.maxProfiles, 'profile')}`)));
  const save = h('button', { class: 'primary', type: 'button', onclick: () => patch(save, {
    ...(a.person ? {} : { label: label.value }),
    expiresAt: expires.value ? new Date(expires.value).toISOString() : '',
    templateId: template.value,
  }, 'Access saved.') }, 'Save access');

  const profiles = a.profiles.map((p) => {
    const unlock = p.pinLocked && h('button', { type: 'button', class: 'small', onclick: () => busy(unlock, async () => {
      await api('POST', `/admin/api/profiles/${p.id}/unlock-pin`, {});
      toast(`${p.name}'s PIN is unlocked.`);
      reload(a.id);
    }) }, 'Unlock PIN');
    return h('li', {}, avatar(p.name), h('div', {},
      h('div', { class: 'name' }, p.name, ' ', p.owner && badge('Owner', 'plain'), ' ', p.kind === 'kids' && badge('Kids', 'info')),
      h('div', { class: 'hint' }, [
        p.kind === 'kids' && (p.movieCeiling && p.seriesCeiling ? `Films up to ${p.movieCeiling}, series up to ${p.seriesCeiling}` : 'Limits not chosen yet'),
        p.hasPin ? (p.pinLocked ? 'PIN locked' : 'PIN set') : 'No PIN',
        p.linked && 'old login still works (linked)',
      ].filter(Boolean).join(' · '))), unlock);
  });
  const devices = a.sessions.map((s) => {
    const out = h('button', { type: 'button', class: 'small', onclick: () => busy(out, async () => {
      await api('DELETE', `/admin/api/accounts/${a.id}/sessions/${s.id}`);
      toast('Signed out.');
      reload(a.id);
    }) }, 'Sign out');
    return h('li', {}, h('div', {}, h('div', {}, s.device || 'Unknown device'), h('div', { class: 'hint' }, `${s.profile} · ${ago(s.lastActiveAt)}`)), out);
  });
  const toggleAccess = h('button', { type: 'button', onclick: () => patch(toggleAccess, { disabled: a.status !== 'disabled' },
    a.status === 'disabled' ? 'Account enabled.' : 'Account disabled.') }, a.status === 'disabled' ? 'Enable account' : 'Disable account');
  const remove = h('button', { type: 'button', class: 'danger', onclick: () => {
    if (!confirm(`Delete ${nameOf(a)}'s account, its profiles and what they hold? This can't be undone.`)) return;
    busy(remove, async () => {
      await api('DELETE', `/admin/api/accounts/${a.id}`);
      toast('Account deleted.');
      reload('');
    });
  } }, 'Delete account');

  return [
    h('div', { class: 'detail-head' }, avatar(nameOf(a), true), h('div', {},
      h('h2', {}, nameOf(a)), h('div', { class: 'hint' }, subline(a) || a.person?.email || ''))),
    h('div', {}, badge(statusText[a.status] || a.status, statusKind[a.status]),
      a.person && !a.person.active && [' ', badge('Off at the identity provider', 'err')]),
    h('h3', {}, 'About'),
    h('dl', { class: 'facts' },
      h('dt', {}, 'Since'), h('dd', {}, when(a.createdAt)),
      h('dt', {}, 'Last active'), h('dd', {}, ago(lastActive(a)) || 'never'),
      h('dt', {}, 'Sign-in ID'), h('dd', { class: 'hint' }, a.subject)),
    signsIn && [h('h3', {}, 'Signing in'), h('div', { class: 'box' }, personControls(signsIn, () => reload(a.id)))],
    h('h3', {}, 'Access'),
    h('div', { class: 'fields' },
      !a.person && field('Label', label, 'A name for it, until the identity provider gives one.', true),
      field('Template', template, 'What the account may do.', true),
      field('Access ends', expires, 'Empty: never.', true)),
    h('div', { class: 'actions-row' }, save),
    h('h3', {}, `Profiles (${a.profiles.length})`), h('ul', { class: 'list' }, profiles),
    h('h3', {}, `Signed-in devices (${a.sessions.length})`),
    devices.length ? h('ul', { class: 'list' }, devices) : h('p', { class: 'hint' }, 'None.'),
    h('div', { class: 'danger-zone' }, toggleAccess, remove),
  ];
}

// ---- Templates -----------------------------------------------------------------

// templatesView lists the account templates, each editable in place.
// Accounts follow theirs live: saving changes every account on it at once.
async function templatesView() {
  const { items } = await api('GET', '/admin/api/templates');
  const templateCard = (t) => {
    const name = h('input', { value: t?.name || '', maxlength: '40', placeholder: 'e.g. Family, Friends' });
    const streams = h('input', { type: 'number', min: '1', max: '100', value: String(t?.maxStreams ?? 1) });
    const profiles = h('input', { type: 'number', min: '1', max: '20', value: String(t?.maxProfiles ?? 1) });
    const body = () => ({ name: name.value, maxStreams: Number(streams.value), maxProfiles: Number(profiles.value) });
    const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
      await (t ? api('PUT', `/admin/api/templates/${t.id}`, body()) : api('POST', '/admin/api/templates', body()));
      toast(t ? 'Template saved.' : 'Template made.');
      templatesView();
    }) }, t ? 'Save' : 'Make it');
    const remove = t && !t.default && h('button', { class: 'danger', type: 'button', onclick: () => {
      if (confirm(`Delete the template "${t.name}"?`)) {
        busy(remove, async () => { await api('DELETE', `/admin/api/templates/${t.id}`); toast('Template deleted.'); templatesView(); });
      }
    } }, 'Delete');
    return card({
      title: t ? t.name : 'A new template',
      badge: t?.default ? badge('Default', 'info') : null,
      description: t ? `${plural(t.accounts, 'account')} on it${t.accounts ? '; changes apply to them at once' : ''}.` : 'For a group of accounts that may do more, or less.',
    },
    h('div', { class: 'fields' }, field('Name', name, null, true), field('Streams at once', streams), field('Profiles', profiles, 'The owner\'s included.')),
    [h('span', { class: 'grow' }), remove, save]);
  };
  show(pageHead('Templates', 'What accounts may do. Every account follows one: Default, unless you choose another for it in People.'),
    h('div', { class: 'cards' }, items.map(templateCard), templateCard(null)));
}

// ---- Apps ----------------------------------------------------------------------

// appsView lists the keys other apps (Stewarr…) read Overfin's statistics
// with: made here, shown once, revoked here.
async function appsView() {
  const { items } = await api('GET', '/admin/api/apps');
  const name = h('input', { type: 'text', placeholder: 'e.g. Stewarr', maxlength: '64' });
  const made = h('div', {});
  const make = h('button', { type: 'submit', class: 'primary' }, 'Make a key');
  const create = (e) => {
    e.preventDefault();
    busy(make, async () => {
      const k = await api('POST', '/admin/api/apps', { name: name.value, scope: 'watch-stats' });
      await appsView();
      fill($('#made-key'), secret(`The key for ${k.name}. Copy it now: it won't be shown again.`, k.key,
        'The app sends it as "Authorization: Bearer <key>" to GET /apps/v1/watch-stats on this address.'));
    });
  };
  const rows = items.map((k) => {
    const revoke = h('button', { type: 'button', class: 'small danger', onclick: () => {
      if (!confirm(`Revoke ${k.name}'s key? The app stops getting anything.`)) return;
      busy(revoke, async () => { await api('DELETE', `/admin/api/apps/${k.id}`); toast('Key revoked.'); appsView(); });
    } }, 'Revoke');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, k.name), h('div', { class: 'hint' }, 'Watch statistics')),
      h('td', { class: 'hint' }, day(k.createdAt)),
      h('td', { class: 'hint' }, k.lastUsedAt ? ago(k.lastUsedAt) : 'never'),
      h('td', { class: 'actions' }, revoke));
  });
  made.id = 'made-key';
  show(pageHead('Apps', 'Other apps (Stewarr…) can read how much each film and series is watched: plays, viewers, favorites, the day last played. Totals per title only, never anything about a person.'),
    made,
    card({ title: 'Keys' },
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'App'), h('th', {}, 'Made'), h('th', {}, 'Last used'), h('th', {}))),
        h('tbody', {}, rows.length ? rows : h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'No app has a key yet.'))))),
      h('form', { class: 'toolbar spaced', onsubmit: create }, field('A new key, for', name), make)));
}

// ---- Identification ------------------------------------------------------------

// identificationView lists every film and series with the TMDB title it's
// taken for, those that may be wrong first and why. A title is fixed by
// picking the right one on TMDB, confirmed by keeping its own, or given
// back to Jellyfin's.
async function identificationView(selected = '') {
  const { items, suspects } = await api('GET', '/admin/api/identification');
  $('#main').classList.add('wide');
  const search = h('input', { type: 'search', placeholder: 'Search titles, folders, files', 'aria-label': 'Search' });
  const filter = h('select', { 'aria-label': 'Show' },
    h('option', { value: 'suspects' }, `To check (${suspects})`),
    h('option', { value: '' }, `All (${items.length})`),
    h('option', { value: 'fixed' }, 'Fixed here'));
  if (!suspects) filter.value = '';
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('div', { class: 'placeholder' }, 'Select a title to check it.'));
  const titleOf = (t) => (t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : t.name);

  const select = (t) => {
    selected = t.itemId;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === t.itemId);
    fill(detail, identificationDetail(t, () => identificationView(t.itemId)));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((t) => (filter.value !== 'suspects' || t.reasons.length) && (filter.value !== 'fixed' || t.overridden) &&
      (!q || [t.name, t.tmdbName, t.originalName, t.folderName, t.fileName, t.tmdb].some((v) => (v || '').toLowerCase().includes(q))));
    fill(tbody, shown.slice(0, 500).map((t) => h('tr', { 'data-id': t.itemId, onclick: () => select(t) },
      h('td', {}, h('div', { class: 'name' }, titleOf(t)), h('div', { class: 'hint' }, t.folderName || t.fileName || '')),
      h('td', { class: 'hide-narrow' }, t.kind === 'movie' ? 'Film' : 'Series'),
      h('td', {}, t.overridden ? badge('Fixed here', 'ok')
        : t.reasons.length ? badge(t.reasons.length === 1 ? reasonName(t.reasons[0].code) : `${t.reasons.length} doubts`, 'warn') : h('span', { class: 'hint' }, 'Looks right')))));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'empty' }, filter.value === 'suspects' ? 'Nothing to check: every title looks right.' : 'Nothing matches.')));
    if (shown.length > 500) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'empty' }, `${shown.length - 500} more: search to narrow.`)));
    const current = items.find((t) => t.itemId === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);
  show(
    pageHead('Identification', 'Each film and series, as the TMDB title Jellyfin matched it to; those that may be wrong come first, with why. A fix is kept by Overfin: Jellyfin isn\'t changed, and its scans don\'t undo it.'),
    h('div', { class: 'split' },
      h('div', {}, h('div', { class: 'toolbar' }, search, filter),
        h('div', { class: 'table-wrap' }, h('table', {},
          h('thead', {}, h('tr', {}, h('th', {}, 'Title'), h('th', { class: 'hide-narrow' }, 'Kind'), h('th', {}, 'Check'))), tbody))),
      detail));
  draw();
}

const reasonNames = { 'no-tmdb': 'No TMDB title', 'tmdb-tag': 'Tagged', name: 'Name differs', year: 'Year differs', seasons: 'Seasons differ', runtime: 'Length differs' };
const reasonName = (code) => reasonNames[code] || code;

// identificationDetail is the selected title: its files, the TMDB title
// it's taken for, why it may be wrong, and TMDB searched for the right one.
function identificationDetail(t, reload) {
  const act = (button, method, body, done) => busy(button, async () => {
    await api(method, `/admin/api/identification/${t.itemId}`, body);
    toast(done);
    await reload();
  });
  const facts = [
    t.folderName && ['Folder', t.folderName],
    t.fileName && ['File', t.fileName],
    t.runtimeMin && ['Length', `${t.runtimeMin} min`],
    t.episodes && ['Episodes here', String(t.episodes)],
    ['TMDB', t.tmdb ? `${t.tmdb}${t.overridden && t.jellyfinTmdb !== t.tmdb ? ` (Jellyfin said ${t.jellyfinTmdb || 'none'})` : ''}` : 'none'],
    t.originalName && ['Original title', `${t.originalName}${t.language ? ` (${t.language})` : ''}`],
  ].filter(Boolean);

  // TMDB, searched from the folder's name by default.
  const guess = (t.folderName || t.fileName || t.name).replace(/\[[^\]]*\]|\{[^}]*\}/g, '').replace(/\.[a-z0-9]{2,4}$/i, '').replace(/[._]/g, ' ')
    .replace(/^(.+?)\s*\(?\b(?:19|20)\d{2}\b\)?\s*$/, '$1').trim();
  const query = h('input', { type: 'search', value: guess, 'aria-label': 'Search TMDB' });
  const results = h('ul', { class: 'list candidates' });
  const searchButton = h('button', { type: 'submit' }, 'Search');
  const find = (e) => {
    e?.preventDefault();
    busy(searchButton, async () => {
      fill(results, h('li', { class: 'hint' }, 'Searching…'));
      const { items } = await api('POST', '/admin/api/identification/search', { query: query.value, kind: t.kind });
      fill(results, items.map((c) => {
        const use = c.tmdb !== t.tmdb && h('button', { type: 'button', class: 'small', onclick: () => act(use, 'PUT', { tmdb: c.tmdb }, 'Title fixed.') }, 'Use this');
        return h('li', {},
          c.poster ? h('img', { src: c.poster, alt: '', loading: 'lazy' }) : h('span', { class: 'noposter' }),
          h('div', {},
            h('div', { class: 'name' }, `${c.name}${c.year ? ` (${c.year})` : ''}`),
            h('div', { class: 'hint' }, [c.originalName && c.originalName !== c.name ? c.originalName : '', c.language, `TMDB ${c.tmdb}`].filter(Boolean).join(' · ')),
            c.overview && h('div', { class: 'hint clamp' }, c.overview)),
          c.tmdb === t.tmdb ? badge('Current', 'info') : use);
      }));
      if (!items.length) results.append(h('li', { class: 'hint' }, 'TMDB found nothing.'));
    }, (err) => fill(results, h('li', { class: 'error' }, err.message)));
  };
  const right = t.reasons.length && t.tmdb && h('button', { type: 'button', onclick: () => act(right, 'PUT', { tmdb: t.tmdb }, 'Kept as it is.') }, 'It\'s right');
  const back = t.overridden && h('button', { type: 'button', onclick: () => act(back, 'DELETE', undefined, 'Back to Jellyfin\'s title.') }, 'Back to Jellyfin\'s');

  return [
    h('div', { class: 'detail-head' }, h('div', {},
      h('h2', {}, t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : 'No TMDB title'),
      h('div', { class: 'hint' }, t.kind === 'movie' ? 'Film' : 'Series'))),
    t.overridden && badge('Fixed here', 'ok'),
    h('h3', {}, 'About'),
    h('dl', { class: 'facts' }, facts.map(([k, v]) => [h('dt', {}, k), h('dd', {}, v)])),
    t.reasons.length > 0 && h('div', { class: 'notice warn spaced' }, h('strong', {}, 'Why it may be wrong'),
      h('ul', {}, t.reasons.map((r) => h('li', {}, r.text)))),
    (right || back) && h('div', { class: 'actions-row' }, right, back),
    h('h3', {}, 'Find the right one on TMDB'),
    h('form', { onsubmit: find, class: 'toolbar' }, query, searchButton),
    results,
  ];
}

// ---- Modules -------------------------------------------------------------------

// moduleView opens a switched-on module's own tab: its panel.js renders into
// the page, with helpers for its admin routes.
async function moduleView(name, description) {
  const root = h('div', {});
  show(pageHead(featureName(name), description), root);
  const tab = await import(`/admin/modules/${encodeURIComponent(name)}/panel.js`);
  await tab.default(root, {
    api: (method, path, body) => api(method, `/admin/api/modules/${encodeURIComponent(name)}${path}`, body),
    h,
    // The panel's own building blocks, so a tab looks like the rest.
    ui: { card, field, setting, badge, toast, busy, secret, status, fill, plural, day, ago },
  });
}

// ---- Layout --------------------------------------------------------------------

function show(...nodes) {
  fill($('#main'), nodes);
  window.scrollTo(0, 0);
}

const views = {
  server: { label: 'Server', icon: icons.server, view: () => serverView() },
  features: { label: 'Features & limits', icon: icons.sliders, view: () => featuresView() },
  people: { label: 'People', icon: icons.people, view: () => peopleView() },
  templates: { label: 'Templates', icon: icons.layers, view: () => templatesView() },
  libraries: { label: 'Libraries', icon: icons.folder, view: () => librariesView() },
  identification: { label: 'Identification', icon: icons.check, view: () => identificationView() },
  apps: { label: 'Apps', icon: icons.key, view: () => appsView() },
};
// Older addresses still lead somewhere.
const aliases = { settings: 'server', users: 'people' };
const moduleIcons = { invites: icons.mail, migration: icons.move };
let modules = [];

function link(tab, label, ic) {
  return h('a', { href: `#/${tab}`, 'data-tab': tab }, icon(ic), h('span', {}, label));
}

async function route() {
  let tab = location.hash.replace(/^#\/?/, '') || 'server';
  tab = aliases[tab] || tab;
  for (const a of document.querySelectorAll('nav a')) a.classList.toggle('active', a.dataset.tab === tab);
  $('#main').classList.remove('wide');
  fill($('#main'), h('p', { class: 'loading' }, 'Loading…'));
  if (tab.startsWith('m/')) {
    const name = decodeURIComponent(tab.slice(2));
    return moduleView(name, modules.find((m) => m.name === name)?.description);
  }
  return (views[tab] || views.server).view();
}

async function start() {
  const who = await api('GET', '/admin/api/session');
  $('#who').textContent = who.name ? `Signed in as ${who.name}` : 'Signed in';
  ({ items: modules } = await api('GET', '/admin/api/modules'));
  const tabs = modules.filter((m) => m.hasTab);
  fill($('nav'),
    h('div', { class: 'group' }, 'Server'),
    link('server', views.server.label, views.server.icon),
    link('features', views.features.label, views.features.icon),
    h('div', { class: 'group' }, 'People'),
    link('people', views.people.label, views.people.icon),
    link('templates', views.templates.label, views.templates.icon),
    tabs.map((m) => link(`m/${encodeURIComponent(m.name)}`, featureName(m.name), moduleIcons[m.name] || icons.puzzle)),
    h('div', { class: 'group' }, 'Library'),
    link('libraries', views.libraries.label, views.libraries.icon),
    link('identification', views.identification.label, views.identification.icon),
    link('apps', views.apps.label, views.apps.icon));
  $('nav').hidden = false;
  $('.me').hidden = false;
  window.addEventListener('hashchange', () => route().catch(fail));
  await route();
}

function fail(err) {
  if (err.message !== 'signing in') show(h('div', { class: 'notice warn' }, err.message));
}

$('#signout').addEventListener('click', async () => {
  await api('POST', '/admin/api/logout', {}).catch(() => {});
  $('nav').hidden = true;
  $('.me').hidden = true;
  show(h('div', { class: 'center' }, h('h1', {}, 'Signed out'), h('p', { class: 'muted' }, 'You left the control panel.'),
    h('p', {}, h('a', { class: 'button', href: '/admin/login?return=/admin/' }, 'Sign in again'))));
});

start().catch(fail);
