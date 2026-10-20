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
  $('#main').replaceChildren(...sections, h('div', { class: 'bar' }, save, restored, msg));
}

async function start() {
  const who = await api('GET', '/admin/api/session');
  $('#who').textContent = who.name ? `Signed in as ${who.name}` : 'Signed in';
  $('#signout').hidden = false;
  const state = await api('GET', '/admin/api/features');
  let draft = null;
  try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
  render(state, draft);
}

$('#signout').addEventListener('click', async () => {
  await api('POST', '/admin/api/logout', {}).catch(() => {});
  $('#main').replaceChildren(h('div', { class: 'center' }, h('p', {}, 'Signed out.'),
    h('a', { href: '/admin/login?return=/admin/' }, 'Sign in again')));
  $('#signout').hidden = true;
  $('#who').textContent = '';
});

start().catch((err) => {
  if (err.message !== 'signing in') {
    $('#main').replaceChildren(h('p', { class: 'error' }, err.message));
  }
});
