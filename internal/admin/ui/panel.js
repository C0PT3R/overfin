// Overfin control panel: the feature settings, switched on and off live.
// Text from the server is only ever set with textContent.

const $ = (sel) => document.querySelector(sel);

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

const DRAFT = 'overfin-panel-draft';

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
    const err = new Error(data.detail || data.type || `HTTP ${res.status}`);
    err.type = data.type;
    throw err;
  }
  return data;
}

// The form's fields, each tied to one setting.
function field(get, set, input) {
  return { get, set, input };
}

function render(state, draft) {
  const f = structuredClone(draft || state.features);
  const fields = [];
  const num = (label, hint, path, min) => {
    const input = h('input', { type: 'number', min: String(min ?? 0) });
    fields.push(field(() => Number(input.value), (v) => { input.value = v; }, input));
    fields.at(-1).path = path;
    return h('div', { class: 'row' }, h('div', {}, label, hint && h('div', { class: 'hint' }, hint)), input);
  };
  const choice = (label, hint, path, options) => {
    const input = h('select', {}, options.map(([v, text]) => h('option', { value: v }, text)));
    fields.push(field(() => input.value, (v) => { input.value = v; }, input));
    fields.at(-1).path = path;
    return h('div', { class: 'row' }, h('div', {}, label, hint && h('div', { class: 'hint' }, hint)), input);
  };
  const toggle = (label, hint, path) => {
    const input = h('input', { type: 'checkbox' });
    fields.push(field(() => input.checked, (v) => { input.checked = v; }, input));
    fields.at(-1).path = path;
    return h('div', { class: 'row' }, h('div', {}, label, hint && h('div', { class: 'hint' }, hint)), input);
  };
  // Lists (plugins, exposed fields) are sets of checkboxes.
  const listToggle = (label, hint, path, value) => {
    const input = h('input', { type: 'checkbox' });
    fields.push({
      path, value, input,
      get: () => input.checked, set: (list) => { input.checked = (list || []).includes(value); },
    });
    return h('div', { class: 'row' }, h('div', {}, label, hint && h('div', { class: 'hint' }, hint)), input);
  };

  const sections = [
    h('section', {}, h('h2', {}, 'Plugins'),
      ...state.plugins.map((p) => listToggle(p.name,
        { library: 'Libraries, items, posters, continue watching', playback: 'Watching, with subtitles' }[p.name] +
        (p.needs ? ` (needs ${p.needs.join(', ')})` : ''), 'plugins.enable', p.name))),
    h('section', {}, h('h2', {}, 'Accounts'),
      toggle('Bring existing accounts', 'People bring their Jellyfin account and history with its username and password', 'adoption.claim'),
      choice('When an account is brought', null, 'adoption.mode', [['move', 'Move it (old login stops working)'], ['link', 'Link it (old login keeps working)']]),
      choice('When an account is deleted', null, 'accounts.onDelete', [['delete', 'Delete its history too'], ['disable', 'Keep its history, unusable']]),
      num('Profiles per account', null, 'limits.maxProfilesPerAccount', 1),
      num('Wrong PINs before a lock', '3 to 10', 'limits.pinAttempts', 3)),
    h('section', {}, h('h2', {}, 'Streams'),
      num('Streams per account', null, 'limits.maxStreamsPerAccount', 1),
      choice('One stream too many', null, 'limits.onLimit', [['replace-oldest', 'Replace the oldest'], ['refuse', 'Refuse the new one']]),
      num('Transcodes on the server', '0 means no limit', 'limits.maxTranscodesTotal', 0),
      num('Bitrate cap (kbps)', '0 means no cap', 'limits.maxBitrateKbps', 0)),
    h('section', {}, h('h2', {}, 'Privacy'),
      ...state.exposableFields.map((name) => listToggle(name, 'Hidden unless switched on', 'privacy.expose', name))),
  ];

  // Fill the form from the settings.
  const at = (obj, path) => path.split('.').reduce((o, k) => o?.[k], obj);
  for (const fl of fields) fl.set(at(f, fl.path));

  // Read the form back into settings.
  const collect = () => {
    const out = structuredClone(state.features);
    for (const fl of fields) {
      const keys = fl.path.split('.');
      const parent = keys.slice(0, -1).reduce((o, k) => o[k], out);
      const key = keys.at(-1);
      if ('value' in fl) {
        const list = new Set(parent[key] || []);
        if (fl.get()) list.add(fl.value); else list.delete(fl.value);
        parent[key] = [...list].sort();
      } else {
        parent[key] = fl.get();
      }
    }
    return out;
  };

  const msg = h('span', {});
  const save = h('button', { class: 'primary', onclick: async () => {
    msg.className = '';
    msg.textContent = 'Saving…';
    const wanted = collect();
    try {
      const out = await api('PUT', '/admin/api/features', wanted);
      sessionStorage.removeItem(DRAFT);
      state.features = out.features;
      msg.textContent = 'Saved and applied.';
    } catch (err) {
      if (err.type === 'step-up-required') {
        // Changes need a recent sign-in: keep them, sign in, come back.
        sessionStorage.setItem(DRAFT, JSON.stringify(wanted));
        location.href = '/admin/login?return=/admin/';
        return;
      }
      msg.className = 'error';
      msg.textContent = err.message;
    }
  } }, 'Save');
  const restored = draft && h('span', { class: 'muted' }, 'Your unsaved changes are back; save to apply them.');
  show(...sections, h('div', { class: 'bar' }, save, restored, msg));
}

// ---- Users -----------------------------------------------------------------

const when = (iso) => (iso ? new Date(iso).toLocaleString() : '');
const day = (iso) => (iso ? new Date(iso).toLocaleDateString() : '');

// ago says how long ago, roughly: "5 min", "3 h", "2 d".
function ago(iso) {
  if (!iso) return '';
  const min = Math.round((Date.now() - new Date(iso)) / 60000);
  if (min < 60) return `${Math.max(min, 1)} min ago`;
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

const statusText = { active: 'Active', disabled: 'Disabled', expired: 'Expired' };
const lastActive = (a) => a.sessions.map((s) => s.lastActiveAt).sort().at(-1) || '';
const nameOf = (a) => a.label || a.profiles.find((p) => p.owner)?.name || 'Unnamed';

// The users view: a table of accounts, and the selected one's details.
async function users(selected = '') {
  const { items } = await api('GET', '/admin/api/accounts');
  $('#main').classList.add('wide');
  const search = h('input', { type: 'search', placeholder: 'Search', class: 'search' });
  const filter = h('select', {}, h('option', { value: '' }, 'All'),
    ...Object.entries(statusText).map(([v, t]) => h('option', { value: v }, t)));
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('p', { class: 'muted' }, 'Select an account.'));

  const select = (a) => {
    selected = a.id;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === a.id);
    detail.replaceChildren(...accountDetail(a, () => users(a.id)));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((a) => (!filter.value || a.status === filter.value) &&
      (!q || [a.label, a.subject, a.id, ...a.profiles.map((p) => p.name)].some((v) => (v || '').toLowerCase().includes(q))));
    tbody.replaceChildren(...shown.map((a) => {
      const kids = a.profiles.filter((p) => p.kind === 'kids').length;
      const tr = h('tr', { 'data-id': a.id, onclick: () => select(a) },
        h('td', {}, h('div', { class: 'name' }, nameOf(a)), a.label ? null : h('div', { class: 'hint' }, 'no label')),
        h('td', {}, h('span', { class: `pill ${a.status}` }, statusText[a.status] || a.status)),
        h('td', {}, String(a.profiles.length), kids ? h('span', { class: 'hint' }, ` (${kids} kids)`) : null),
        h('td', {}, String(a.sessions.length)),
        h('td', { class: 'hint' }, ago(lastActive(a)) || '—'),
        h('td', { class: 'hint' }, day(a.createdAt)));
      return tr;
    }));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '6', class: 'muted' }, 'No accounts match.')));
    const current = items.find((a) => a.id === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);

  const table = h('table', {},
    h('thead', {}, h('tr', {}, ...['Account', 'Status', 'Profiles', 'Devices', 'Last active', 'Since'].map((t) => h('th', {}, t)))),
    tbody);
  show(h('div', { class: 'users' },
    h('div', {}, h('div', { class: 'toolbar' }, h('h2', {}, `Accounts (${items.length})`), search, filter), table),
    detail));
  draw();
}

// accountDetail is the selected account's panel: access, profiles, devices,
// and the destructive actions, set apart.
function accountDetail(a, reload) {
  const msg = h('p', {});
  const run = async (action) => {
    msg.className = '';
    msg.textContent = '';
    try {
      await action();
      reload();
    } catch (err) {
      msg.className = 'error';
      msg.textContent = err.message;
    }
  };
  const patch = (change) => run(() => api('PATCH', `/admin/api/accounts/${a.id}`, change));

  const label = h('input', { value: a.label || '', maxlength: '64', placeholder: 'e.g. Bob, or "the cabin"' });
  const expires = h('input', { type: 'datetime-local', value: toLocalInput(a.expiresAt) });
  const streams = h('input', { type: 'number', min: '0', max: '100', value: String(a.maxStreams || 0) });

  const field = (text, hint, input) => h('label', { class: 'field' }, h('span', {}, text, hint && h('span', { class: 'hint' }, ` ${hint}`)), input);
  const profiles = a.profiles.map((p) => h('li', {},
    h('div', {}, h('strong', {}, p.name), p.owner ? h('span', { class: 'hint' }, ' owner') : null),
    h('div', { class: 'hint' }, [
      p.kind === 'kids' ? `Kids, up to ${p.ratingCeiling}` : 'Standard',
      p.hasPin ? (p.pinLocked ? 'PIN locked' : 'PIN set') : 'No PIN',
      p.linked ? 'old login still works (linked)' : '',
    ].filter(Boolean).join(' · ')),
    p.pinLocked && h('button', { onclick: () => run(() => api('POST', `/admin/api/profiles/${p.id}/unlock-pin`, {})) }, 'Unlock PIN')));
  const devices = a.sessions.map((s) => h('li', {},
    h('div', {}, s.device || 'Unknown device'),
    h('div', { class: 'hint' }, `${s.profile} · ${ago(s.lastActiveAt)}`),
    h('button', { onclick: () => run(() => api('DELETE', `/admin/api/accounts/${a.id}/sessions/${s.id}`)) }, 'Sign out')));

  return [
    h('h2', {}, nameOf(a), ' ', h('span', { class: `pill ${a.status}` }, statusText[a.status] || a.status)),
    h('p', { class: 'hint' }, `Since ${when(a.createdAt)}`, h('br'), `Identity provider ID: ${a.subject}`),
    h('h3', {}, 'Access'),
    field('Label', '(your note)', label),
    field('Access ends', '(empty: never)', expires),
    field('Streams at once', "(0: the server's limit)", streams),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => patch({
      label: label.value,
      expiresAt: expires.value ? new Date(expires.value).toISOString() : '',
      maxStreams: Number(streams.value),
    }) }, 'Save'), msg),
    h('h3', {}, `Profiles (${a.profiles.length})`), h('ul', { class: 'items' }, ...profiles),
    h('h3', {}, `Signed-in devices (${a.sessions.length})`),
    devices.length ? h('ul', { class: 'items' }, ...devices) : h('p', { class: 'muted' }, 'None.'),
    h('div', { class: 'danger-zone' },
      h('button', { onclick: () => patch({ disabled: a.status !== 'disabled' }) },
        a.status === 'disabled' ? 'Enable account' : 'Disable account'),
      h('button', { class: 'danger', onclick: () => {
        if (!confirm(`Delete ${nameOf(a)}, its profiles and what they hold? This can't be undone.`)) return;
        run(() => api('DELETE', `/admin/api/accounts/${a.id}`));
      } }, 'Delete account')),
  ];
}

// ---- Layout ----------------------------------------------------------------

function show(...nodes) {
  $('#main').replaceChildren(...nodes);
}

async function route() {
  const tab = location.hash === '#/users' ? 'users' : 'settings';
  for (const a of document.querySelectorAll('nav a')) a.classList.toggle('active', a.dataset.tab === tab);
  if (tab === 'users') return users();
  $('#main').classList.remove('wide');
  const state = await api('GET', '/admin/api/features');
  let draft = null;
  try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
  render(state, draft);
}

async function start() {
  const who = await api('GET', '/admin/api/session');
  $('#who').textContent = who.name ? `Signed in as ${who.name}` : 'Signed in';
  $('#signout').hidden = false;
  $('nav').hidden = false;
  window.addEventListener('hashchange', () => route().catch(fail));
  await route();
}

function fail(err) {
  if (err.message !== 'signing in') show(h('p', { class: 'error' }, err.message));
}

$('#signout').addEventListener('click', async () => {
  await api('POST', '/admin/api/logout', {}).catch(() => {});
  $('#main').replaceChildren(h('div', { class: 'center' }, h('p', {}, 'Signed out.'),
    h('a', { href: '/admin/login?return=/admin/' }, 'Sign in again')));
  $('#signout').hidden = true;
  $('#who').textContent = '';
});

start().catch(fail);
