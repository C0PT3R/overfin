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

// Jellyfin: where Overfin reaches it, changed apart from the feature
// settings, tested before it's saved.
function jellyfinSection(j) {
  if (!j) return null;
  if (j.inFile) {
    return h('section', {}, h('h2', {}, 'Jellyfin'),
      h('div', { class: 'row' }, h('div', {}, j.url,
        h('div', { class: 'hint' }, "Set in Overfin's configuration file: change it there."))));
  }
  const url = h('input', { value: j.url, autocomplete: 'off', spellcheck: 'false' });
  const key = h('input', { type: 'password', autocomplete: 'off', placeholder: 'Unchanged' });
  const msg = h('span', {});
  const save = h('button', { type: 'button', onclick: async () => {
    msg.className = 'muted';
    msg.textContent = 'Testing…';
    save.disabled = true;
    try {
      const out = await api('PUT', '/admin/api/jellyfin', { url: url.value, key: key.value });
      url.value = out.url;
      key.value = '';
      msg.className = '';
      msg.textContent = `Saved and applied: ${out.summary}.`;
    } catch (err) {
      msg.className = 'error';
      msg.textContent = err.message;
    } finally {
      save.disabled = false;
    }
  } }, 'Test and save');
  return h('section', {}, h('h2', {}, 'Jellyfin'),
    h('div', { class: 'row' }, h('div', {}, "Jellyfin's address",
      h('div', { class: 'hint' }, 'A new address for the same server, as Overfin reaches it. Not localhost: in a container, that\'s Overfin itself.')), url),
    h('div', { class: 'row' }, h('div', {}, 'API key', h('div', { class: 'hint' }, 'Only to replace it; empty keeps the current one.')), key),
    h('div', { class: 'row' }, msg, save));
}

// Seerr: discovery's source, connected, changed or disconnected here,
// tested before it's saved.
function seerrSection(sr) {
  if (!sr) return null;
  const url = h('input', { value: sr.url, autocomplete: 'off', spellcheck: 'false', placeholder: 'http://192.168.1.10:5055' });
  const key = h('input', { type: 'password', autocomplete: 'off', placeholder: sr.url ? 'Unchanged' : '' });
  const msg = h('span', { class: 'muted' }, sr.url ? 'Connected: discovery is on.' : 'Not connected: discovery is off.');
  const send = async (body, button) => {
    msg.className = 'muted';
    msg.textContent = 'Testing…';
    button.disabled = true;
    try {
      const out = await api('PUT', '/admin/api/seerr', body);
      url.value = out.url;
      key.value = '';
      key.placeholder = out.url ? 'Unchanged' : '';
      msg.className = '';
      msg.textContent = out.summary;
    } catch (err) {
      msg.className = 'error';
      msg.textContent = err.message;
    } finally {
      button.disabled = false;
    }
  };
  const save = h('button', { type: 'button', onclick: () => send({ url: url.value, key: key.value }, save) }, 'Test and save');
  const off = h('button', { type: 'button', onclick: () => send({ url: '', key: '' }, off) }, 'Disconnect');
  return h('section', {}, h('h2', {}, 'Seerr'),
    h('div', { class: 'row' }, h('div', {}, "Seerr's address",
      h('div', { class: 'hint' }, 'As Overfin reaches it. Optional: TMDB\'s data comes from TMDB itself; Seerr will bring requests.')), url),
    h('div', { class: 'row' }, h('div', {}, 'API key', h('div', { class: 'hint' }, "In Seerr's settings, under General.")), key),
    h('div', { class: 'row' }, msg, h('span', {}, sr.url ? off : null, ' ', save)));
}

// Ratings: Overfin's own, from TMDB, which kids profiles are held to. The
// country is picked here; changing it clears kids profiles' ceilings.
function ratingsSection(rt) {
  if (!rt) return null;
  let names = null;
  try { names = new Intl.DisplayNames([navigator.language, 'en'], { type: 'region' }); } catch { names = null; }
  const label = (cc) => (names && names.of(cc)) || cc;
  const countries = rt.countries.length ? rt.countries : [rt.country];
  const select = h('select', {}, countries.map((cc) => h('option', { value: cc }, `${label(cc)} (${cc})`)));
  select.value = rt.country;
  let langNames = null;
  try { langNames = new Intl.DisplayNames([navigator.language, 'en'], { type: 'language' }); } catch { langNames = null; }
  const languages = ['ar', 'cs', 'da', 'de', 'el', 'en', 'es', 'fi', 'fr', 'he', 'hi', 'hu', 'id', 'it', 'ja', 'ko', 'nl', 'no',
    'pl', 'pt', 'ro', 'ru', 'sv', 'th', 'tr', 'uk', 'vi', 'zh'];
  if (!languages.includes(rt.language)) languages.push(rt.language);
  const lang = h('select', {}, languages.map((l) => h('option', { value: l }, `${(langNames && langNames.of(l)) || l} (${l})`)));
  lang.value = rt.language;
  const shown = h('select', {}, [['all', 'All, announced ones too'], ['released', 'Released (in theatres too)'],
    ['home', 'Watchable at home (digital, disc or TV)']].map(([v, t]) => h('option', { value: v }, t)));
  shown.value = rt.shown;
  const status = (r) => {
    if (!r.source) return 'TMDB can\'t be reached: no title can be rated, so kids profiles see nothing.';
    if (!r.countries.length) return 'Fetching the scales from TMDB… Kids profiles see nothing until titles are rated.';
    return `${r.fetched} of the library's ${r.total} films and series rated. Kids profiles see only rated titles within their limits.`;
  };
  const msg = h('span', { class: 'muted' }, status(rt));
  const save = h('button', { type: 'button', disabled: !rt.countries.length }, 'Save');
  save.onclick = async () => {
    if (select.value === rt.country && lang.value === rt.language && shown.value === rt.shown) return;
    if (select.value !== rt.country &&
      !confirm('Every kids profile will need new limits from its account\'s owner, and sees nothing until then. Change the country?')) {
      select.value = rt.country;
      return;
    }
    save.disabled = true;
    try {
      const out = await api('PUT', '/admin/api/ratings', { country: select.value, language: lang.value, shown: shown.value });
      rt.country = out.country;
      rt.language = out.language;
      rt.shown = out.shown;
      msg.className = 'muted';
      msg.textContent = status(out);
    } catch (err) {
      msg.className = 'error';
      msg.textContent = err.message;
    } finally {
      save.disabled = false;
    }
  };
  return h('section', {}, h('h2', {}, 'TMDB'),
    h('div', { class: 'row' }, h('div', {}, 'Ratings country',
      h('div', { class: 'hint' }, "Kids profiles' limits use this country's ratings, from TMDB. A title with none there is hidden from them.")), select),
    h('div', { class: 'row' }, h('div', {}, 'Language',
      h('div', { class: 'hint' }, "Discovery's titles and the artwork's text (posters, logos), when TMDB has them in it.")), lang),
    h('div', { class: 'row' }, h('div', {}, 'Titles shown',
      h('div', { class: 'hint' }, 'Of those no library has: in discovery, on their pages, in requests. A title a library has is always shown.')), shown),
    h('div', { class: 'row' }, msg, save));
}

// The catalog's index: read only, where it stands.
function catalogSection(c) {
  if (!c) return null;
  const t = c.titles || {};
  const counts = [['movie', 'films'], ['series', 'series'], ['season', 'seasons'], ['episode', 'episodes']]
    .map(([k, label]) => `${t[k] || 0} ${label}`).join(', ');
  const read = c.fullAt && !c.fullAt.startsWith('0001')
    ? `Library read in full ${ago(c.fullAt)}; changes read ${ago(c.changesAt)}.`
    : 'The library is being read for the first time.';
  const u = c.unmatched || {};
  const parts = [
    [u.movie, 'films', 'without a TMDB ID'], [u.series, 'series', 'without a TMDB ID'],
    [u.season, 'seasons', 'without their number'], [u.episode, 'episodes', 'without their season or episode number'],
  ].filter(([n]) => n > 0).map(([n, label, why]) => `${n} ${label} ${why}`);
  const matching = parts.length
    ? `Can't be matched yet: ${parts.join('; ')}. Refreshing their metadata in Jellyfin usually fixes it.`
    : 'Every title can be matched.';
  return h('section', {}, h('h2', {}, 'Catalog'),
    h('div', { class: 'row' }, h('div', {}, counts, h('div', { class: 'hint' }, read))),
    h('div', { class: 'row' }, h('div', { class: 'hint' }, matching)),
    c.error && h('div', { class: 'row' }, h('div', { class: 'error' }, `Last read failed: ${c.error}`)));
}

function render(state, draft) {
  // A draft fills in over the current settings: one kept from an older
  // version may lack sections, or have some that are gone.
  const f = structuredClone({ ...state.features,
    ...Object.fromEntries(Object.entries(draft || {}).filter(([k]) => k in state.features)) });
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
  // A module's switch setting is "on" or "off".
  const onOff = (label, hint, path) => {
    const input = h('input', { type: 'checkbox' });
    fields.push(field(() => (input.checked ? 'on' : 'off'), (v) => { input.checked = v === 'on'; }, input));
    fields.at(-1).path = path;
    return h('div', { class: 'row sub' }, h('div', {}, label, hint && h('div', { class: 'hint' }, hint)), input);
  };
  // A module's settings, under its switch.
  const moduleSettings = (p) => (p.settings || []).map((s) => {
    const path = `modules.${p.name}.${s.key}`;
    if (!s.choices) return onOff(s.label, s.hint, path);
    const row = choice(s.label, s.hint, path, s.choices.map((c) => [c.value, c.label]));
    row.classList.add('sub');
    return row;
  });
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
    jellyfinSection(state.jellyfin),
    catalogSection(state.catalog),
    seerrSection(state.seerr),
    ratingsSection(state.ratings),
    h('section', {}, h('h2', {}, 'Features'),
      ...state.plugins.flatMap((p) => [listToggle(p.name,
        (p.description || '') +
        (p.needs ? ` (needs ${p.needs.join(', ')})` : ''), 'plugins.enable', p.name),
      ...moduleSettings(p)])),
    h('section', {}, h('h2', {}, 'Accounts'),
      choice('When an account is deleted', null, 'accounts.onDelete', [['delete', 'Delete its history too'], ['disable', 'Keep its history, unusable']]),
      num('Wrong PINs before a lock', '3 to 10', 'limits.pinAttempts', 3)),
    h('section', {}, h('h2', {}, 'Streams'),
      choice('One stream too many', null, 'limits.onLimit', [['replace-oldest', 'Replace the oldest'], ['refuse', 'Refuse the new one']]),
      num('Transcodes on the server', 'Re-encoded video only, not repackaging; 0 means no limit', 'limits.maxTranscodesTotal', 0),
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
  show(...sections.filter(Boolean), h('div', { class: 'bar' }, save, restored, msg));
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
// nameOf is the best name for an account: the identity provider's, then the
// operator's label, then the owner profile's name. Labels only stand in for
// the provider's names: when it gives one, the label isn't shown.
const nameOf = (a) => a.person?.name || a.person?.username || a.label || a.profiles.find((p) => p.owner)?.name || 'Unnamed';
const subline = (a) => [a.person?.username && a.person.name ? a.person.username : '', a.person?.email || ''].filter(Boolean).join(' · ');

// The users view: a table of accounts, and the selected one's details.
async function users(selected = '') {
  const [{ items, directory }, { items: templates }] = await Promise.all([
    api('GET', '/admin/api/accounts'), api('GET', '/admin/api/templates')]);
  $('#main').classList.add('wide');
  const search = h('input', { type: 'search', placeholder: 'Search', class: 'search' });
  const filter = h('select', {}, h('option', { value: '' }, 'All'),
    ...Object.entries(statusText).map(([v, t]) => h('option', { value: v }, t)));
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('p', { class: 'muted' }, 'Select an account.'));

  const select = (a) => {
    selected = a.id;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === a.id);
    detail.replaceChildren(...accountDetail(a, templates, () => users(a.id)));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((a) => (!filter.value || a.status === filter.value) &&
      (!q || [a.label, a.person?.name, a.person?.username, a.person?.email, a.subject, a.id, ...a.profiles.map((p) => p.name)].some((v) => (v || '').toLowerCase().includes(q))));
    tbody.replaceChildren(...shown.map((a) => {
      const kids = a.profiles.filter((p) => p.kind === 'kids').length;
      const tr = h('tr', { 'data-id': a.id, onclick: () => select(a) },
        h('td', {}, h('div', { class: 'name' }, nameOf(a)), h('div', { class: 'hint' }, subline(a) || (a.person ? '' : 'name unknown'))),
        h('td', {}, h('span', { class: `pill ${a.status}` }, statusText[a.status] || a.status),
          a.person && !a.person.active ? h('div', {}, h('span', { class: 'pill disabled' }, 'off in identity provider')) : null),
        h('td', {}, a.template.name),
        h('td', {}, String(a.profiles.length), kids ? h('span', { class: 'hint' }, ` (${kids} kids)`) : null),
        h('td', {}, String(a.sessions.length)),
        h('td', { class: 'hint' }, ago(lastActive(a)) || '—'),
        h('td', { class: 'hint' }, day(a.createdAt)));
      return tr;
    }));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '7', class: 'muted' }, 'No accounts match.')));
    const current = items.find((a) => a.id === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);

  const table = h('table', {},
    h('thead', {}, h('tr', {}, ...['Account', 'Status', 'Template', 'Profiles', 'Devices', 'Last active', 'Since'].map((t) => h('th', {}, t)))),
    tbody);
  const notice = {
    none: "Names come from the identity provider through a connector (see [connector] in the configuration); until then, labels tell accounts apart.",
    unreachable: "The identity provider's connector didn't answer, so names are missing for now. The log says why.",
  }[directory];
  show(h('div', { class: 'users' },
    h('div', {}, h('div', { class: 'toolbar' }, h('h2', {}, `Accounts (${items.length})`), search, filter),
      notice && h('p', { class: 'notice' }, notice), table),
    detail));
  draw();
}

// accountDetail is the selected account's panel: access, profiles, devices,
// and the destructive actions, set apart.
function accountDetail(a, templates, reload) {
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

  const label = h('input', { value: a.label || '', maxlength: '64', placeholder: 'e.g. Bob' });
  const expires = h('input', { type: 'datetime-local', value: toLocalInput(a.expiresAt) });
  const template = h('select', {}, templates.map((t) => h('option', { value: t.id, selected: t.id === a.template.id },
    `${t.name} (${t.maxStreams} stream${t.maxStreams > 1 ? 's' : ''}, ${t.maxProfiles} profile${t.maxProfiles > 1 ? 's' : ''})`)));

  const field = (text, hint, input) => h('label', { class: 'field' }, h('span', {}, text, hint && h('span', { class: 'hint' }, ` ${hint}`)), input);
  const profiles = a.profiles.map((p) => h('li', {},
    h('div', {}, h('strong', {}, p.name), p.owner ? h('span', { class: 'hint' }, ' owner') : null),
    h('div', { class: 'hint' }, [
      p.kind === 'kids' ? (p.movieCeiling && p.seriesCeiling ? `Kids: films up to ${p.movieCeiling}, series up to ${p.seriesCeiling}` : 'Kids, limits not chosen yet') : 'Standard',
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
    a.person && h('div', { class: 'person' },
      h('div', {}, a.person.name || a.person.username),
      h('div', { class: 'hint' }, [a.person.username, a.person.email].filter(Boolean).join(' · ')),
      !a.person.active && h('div', { class: 'error' }, 'Turned off in the identity provider')),
    h('p', { class: 'hint' }, `Since ${when(a.createdAt)}`, h('br'), `Identity provider ID: ${a.subject}`),
    h('h3', {}, 'Access'),
    !a.person && field('Label', '(a name for it, until the identity provider gives one)', label),
    field('Access ends', '(empty: never)', expires),
    field('Template', '(what the account may do)', template),
    h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: () => patch({
      ...(a.person ? {} : { label: label.value }),
      expiresAt: expires.value ? new Date(expires.value).toISOString() : '',
      templateId: template.value,
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

// ---- Templates -------------------------------------------------------------

// templatesView lists the account templates, each editable in place.
// Accounts follow theirs live: saving changes every account on it at once.
async function templatesView() {
  $('#main').classList.remove('wide');
  const { items } = await api('GET', '/admin/api/templates');
  const card = (t) => {
    const msg = h('span', {});
    const name = h('input', { value: t?.name || '', maxlength: '40', placeholder: 'e.g. Family, Friends' });
    const streams = h('input', { type: 'number', min: '1', max: '100', value: String(t?.maxStreams ?? 1) });
    const profiles = h('input', { type: 'number', min: '1', max: '20', value: String(t?.maxProfiles ?? 1) });
    const body = () => ({ name: name.value, maxStreams: Number(streams.value), maxProfiles: Number(profiles.value) });
    const run = async (action) => {
      msg.className = '';
      msg.textContent = '';
      try { await action(); templatesView(); } catch (err) { msg.className = 'error'; msg.textContent = err.message; }
    };
    const save = t
      ? h('button', { class: 'primary', onclick: () => run(() => api('PUT', `/admin/api/templates/${t.id}`, body())) }, 'Save')
      : h('button', { class: 'primary', onclick: () => run(() => api('POST', '/admin/api/templates', body())) }, 'Create');
    const remove = t && !t.default && h('button', { class: 'danger', onclick: () => {
      if (confirm(`Delete the template "${t.name}"?`)) run(() => api('DELETE', `/admin/api/templates/${t.id}`));
    } }, 'Delete');
    return h('section', {},
      h('h2', {}, t ? t.name : 'New template', t?.default ? h('span', { class: 'pill' }, ' default') : null),
      t && h('p', { class: 'hint' }, `${t.accounts} account${t.accounts === 1 ? '' : 's'} on it${t.accounts ? '; changes apply to them at once' : ''}.`),
      h('div', { class: 'row' }, h('div', {}, 'Name'), name),
      h('div', { class: 'row' }, h('div', {}, 'Streams at once'), streams),
      h('div', { class: 'row' }, h('div', {}, 'Profiles', h('div', { class: 'hint' }, "the owner's included")), profiles),
      h('div', { class: 'bar' }, save, remove, msg));
  };
  show(h('p', { class: 'notice' }, 'Templates say what accounts may do. Every account follows one: Default, unless you choose another for it in Users.'),
    ...items.map(card), card(null));
}

// ---- Identification ----------------------------------------------------------

// identification lists every film and series of the libraries with the
// TMDB title it's taken for, those that may be wrong first and why. A
// title is fixed by picking the right one on TMDB, confirmed by keeping
// its own, or given back to Jellyfin's.
async function identification(selected = '') {
  const { items, suspects } = await api('GET', '/admin/api/identification');
  $('#main').classList.add('wide');
  const search = h('input', { type: 'search', placeholder: 'Search', class: 'search' });
  const filter = h('select', {},
    h('option', { value: 'suspects' }, `To check (${suspects})`),
    h('option', { value: '' }, `All (${items.length})`),
    h('option', { value: 'fixed' }, 'Fixed here'));
  if (!suspects) filter.value = '';
  const tbody = h('tbody', {});
  const detail = h('aside', { class: 'detail' }, h('p', { class: 'muted' }, 'Select a title.'));
  const titleOf = (t) => (t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : t.name);

  const select = (t) => {
    selected = t.itemId;
    for (const tr of tbody.children) tr.classList.toggle('selected', tr.dataset.id === t.itemId);
    detail.replaceChildren(...identificationDetail(t, () => identification(t.itemId)));
  };
  const draw = () => {
    const q = search.value.trim().toLowerCase();
    const shown = items.filter((t) => (filter.value !== 'suspects' || t.reasons.length) && (filter.value !== 'fixed' || t.overridden) &&
      (!q || [t.name, t.tmdbName, t.originalName, t.folderName, t.fileName, t.tmdb].some((v) => (v || '').toLowerCase().includes(q))));
    tbody.replaceChildren(...shown.slice(0, 500).map((t) => h('tr', { 'data-id': t.itemId, onclick: () => select(t) },
      h('td', {}, h('div', { class: 'name' }, titleOf(t)), h('div', { class: 'hint' }, t.folderName || t.fileName || '')),
      h('td', {}, t.kind === 'movie' ? 'Film' : 'Series'),
      h('td', {}, t.overridden ? h('span', { class: 'pill active' }, 'Fixed here')
        : t.reasons.length ? h('span', { class: 'pill disabled' }, t.reasons.map((r) => r.code).join(', ')) : h('span', { class: 'hint' }, '—')))));
    if (!shown.length) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'muted' }, 'Nothing to show.')));
    if (shown.length > 500) tbody.append(h('tr', {}, h('td', { colspan: '3', class: 'muted' }, `${shown.length - 500} more: search to narrow.`)));
    const current = items.find((t) => t.itemId === selected);
    if (current) select(current);
  };
  search.addEventListener('input', draw);
  filter.addEventListener('change', draw);
  show(h('div', { class: 'users' },
    h('div', {}, h('div', { class: 'toolbar' }, h('h2', {}, 'Identification'), search, filter),
      h('p', { class: 'notice' }, "Each film and series is shown as the TMDB title Jellyfin matched it to. Those that may be wrong come first, with why. A fix is kept by Overfin: Jellyfin isn't changed, and its scans don't undo it."),
      h('table', {}, h('thead', {}, h('tr', {}, ...['Title', 'Kind', 'Check'].map((x) => h('th', {}, x)))), tbody)),
    detail));
  draw();
}

// identificationDetail is the selected title: its files, the TMDB title
// it's taken for, why it may be wrong, and TMDB searched for the right one.
function identificationDetail(t, reload) {
  const msg = h('p', {});
  const act = async (method, body) => {
    msg.className = '';
    msg.textContent = 'Saving…';
    try {
      await api(method, `/admin/api/identification/${t.itemId}`, body);
      await reload();
    } catch (err) {
      msg.className = 'error';
      msg.textContent = err.message;
    }
  };
  const facts = [
    t.folderName && ['Folder', t.folderName],
    t.fileName && ['File', t.fileName],
    t.runtimeMin && ['Length', `${t.runtimeMin} min`],
    t.episodes && ['Episodes here', String(t.episodes)],
    ['TMDB', t.tmdb ? `${t.tmdb}${t.overridden && t.jellyfinTmdb !== t.tmdb ? ` (Jellyfin said ${t.jellyfinTmdb || 'none'})` : ''}` : 'none'],
    t.originalName && ['Original title', `${t.originalName}${t.language ? ` (${t.language})` : ''}`],
  ].filter(Boolean);

  // TMDB, searched from the folder's name by default.
  const guess = (t.folderName || t.fileName || t.name).replace(/\[[^\]]*\]|\{[^}]*\}/g, '').replace(/\.[a-z0-9]{2,4}$/i, '').replace(/[._]/g, ' ').replace(/^(.+?)\s*\(?\b(?:19|20)\d{2}\b\)?\s*$/, '$1').trim();
  const query = h('input', { type: 'search', value: guess, class: 'search' });
  const results = h('ul', { class: 'items candidates' });
  const find = async (e) => {
    e?.preventDefault();
    results.replaceChildren(h('li', { class: 'muted' }, 'Searching…'));
    try {
      const { items } = await api('POST', '/admin/api/identification/search', { query: query.value, kind: t.kind });
      results.replaceChildren(...items.map((c) => h('li', {},
        c.poster ? h('img', { src: c.poster, alt: '', loading: 'lazy' }) : h('span', { class: 'noposter' }),
        h('div', {},
          h('div', { class: 'name' }, `${c.name}${c.year ? ` (${c.year})` : ''}`),
          h('div', { class: 'hint' }, [c.originalName && c.originalName !== c.name ? c.originalName : '', c.language, `TMDB ${c.tmdb}`].filter(Boolean).join(' · ')),
          c.overview && h('div', { class: 'hint clamp' }, c.overview)),
        c.tmdb === t.tmdb ? h('span', { class: 'pill' }, 'current')
          : h('button', { type: 'button', onclick: () => act('PUT', { tmdb: c.tmdb }) }, 'Use this one'))));
      if (!items.length) results.append(h('li', { class: 'muted' }, 'TMDB found nothing.'));
    } catch (err) {
      results.replaceChildren(h('li', { class: 'error' }, err.message));
    }
  };

  return [
    h('h2', {}, t.tmdbName ? `${t.tmdbName}${t.tmdbYear ? ` (${t.tmdbYear})` : ''}` : 'No TMDB title',
      t.overridden ? h('span', { class: 'pill active' }, 'Fixed here') : null),
    h('ul', { class: 'items facts' }, ...facts.map(([k, v]) => h('li', {}, h('span', { class: 'hint' }, k), h('span', {}, v)))),
    t.reasons.length ? h('div', { class: 'notice' }, h('strong', {}, 'Why it may be wrong'),
      h('ul', {}, ...t.reasons.map((r) => h('li', {}, r.text)))) : null,
    h('div', { class: 'danger-zone' },
      t.reasons.length && t.tmdb ? h('button', { type: 'button', onclick: () => act('PUT', { tmdb: t.tmdb }) }, "It's right") : null,
      t.overridden ? h('button', { type: 'button', onclick: () => act('DELETE') }, "Back to Jellyfin's") : null),
    msg,
    h('h3', {}, 'Find the right one on TMDB'),
    h('form', { onsubmit: find, class: 'toolbar' }, query, h('button', { type: 'submit' }, 'Search')),
    results,
  ];
}

// ---- Layout ----------------------------------------------------------------

function show(...nodes) {
  $('#main').replaceChildren(...nodes);
}

// moduleView opens a switched-on module's own tab: its panel.js renders into
// the page, with helpers for its admin routes.
async function moduleView(name) {
  $('#main').classList.remove('wide');
  const root = h('div', {});
  show(root);
  const tab = await import(`/admin/modules/${encodeURIComponent(name)}/panel.js`);
  await tab.default(root, {
    api: (method, path, body) => api(method, `/admin/api/modules/${encodeURIComponent(name)}${path}`, body),
    h,
  });
}

async function route() {
  const tab = location.hash.startsWith('#/m/') ? location.hash.slice(1)
    : { '#/users': 'users', '#/templates': 'templates', '#/identification': 'identification' }[location.hash] || 'settings';
  for (const a of document.querySelectorAll('nav a')) a.classList.toggle('active', a.dataset.tab === tab);
  if (tab.startsWith('/m/')) return moduleView(decodeURIComponent(tab.slice(3)));
  if (tab === 'users') return users();
  if (tab === 'templates') return templatesView();
  if (tab === 'identification') return identification();
  $('#main').classList.remove('wide');
  const state = await api('GET', '/admin/api/features');
  state.jellyfin = await api('GET', '/admin/api/jellyfin').catch(() => null);
  state.catalog = await api('GET', '/admin/api/catalog').catch(() => null);
  state.seerr = await api('GET', '/admin/api/seerr').catch(() => null);
  state.ratings = await api('GET', '/admin/api/ratings').catch(() => null);
  let draft = null;
  try { draft = JSON.parse(sessionStorage.getItem(DRAFT) || 'null'); } catch { draft = null; }
  render(state, draft);
}

async function start() {
  const who = await api('GET', '/admin/api/session');
  $('#who').textContent = who.name ? `Signed in as ${who.name}` : 'Signed in';
  $('#signout').hidden = false;
  $('nav').hidden = false;
  // A tab for each switched-on module that has one.
  const { items: mods } = await api('GET', '/admin/api/modules');
  for (const m of mods.filter((x) => x.hasTab)) {
    $('nav').append(h('a', { href: `#/m/${encodeURIComponent(m.name)}`, 'data-tab': `/m/${m.name}`, title: m.description || '' }, m.name));
  }
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
