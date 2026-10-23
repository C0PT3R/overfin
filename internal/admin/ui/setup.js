// Overfin's first-run setup wizard: one step per screen, each saved as it's
// done, with Back and Next. The steps come from the server: those with
// something to ask, in order. A Jellyfin someone else set up takes two
// more rounds on its step: its administrator's login, then a confirmation
// once the page shows what it holds. Text from the server is only ever set
// with textContent.

const $ = (sel) => document.querySelector(sel);
const command = `docker logs overfin | grep -o '"code":"[^"]*' | tail -1 | cut -d'"' -f4`;

let setup = { steps: [], fixedUrl: '', site: false };
let at = 0; // the step shown
let busy = false;
const skipped = new Set();
// The server shown for confirmation, once its administrator signed in.
let shown = null;

const step = () => setup.steps[at];

async function load() {
  try {
    const res = await fetch('/admin/api/setup');
    if (res.ok) setup = await res.json();
  } catch {
    // Shown below: Overfin didn't answer.
  }
  if (!setup.steps.length) {
    fail("Overfin didn't answer. Is it still running?");
    return;
  }
  if (setup.languages) {
    for (const c of setup.languages) $('#language').append(new Option(c.name, c.tag));
    $('#language').value = guessLanguage(setup.languages);
  }
  $('#url-field').hidden = !!setup.fixedUrl;
  $('#fixed').hidden = !setup.fixedUrl;
  $('#fixed').textContent = setup.fixedUrl ? `At ${setup.fixedUrl}, as Overfin's configuration file says.` : '';
  $('#command').value = command;
  show(0);
}

// guessLanguage picks the browser's language among the choices: the same
// tag, else the same language, else English (United States).
function guessLanguage(choices) {
  const tags = choices.map((c) => c.tag);
  for (const want of navigator.languages || [navigator.language]) {
    if (tags.includes(want)) return want;
    const same = tags.find((t) => t.split('-')[0] === want.split('-')[0]);
    if (same) return same;
  }
  return 'en-US';
}

// show displays step i, its buttons and where the wizard stands.
function show(i) {
  at = i;
  const s = step();
  for (const sec of document.querySelectorAll('section[data-step]')) sec.hidden = sec.dataset.step !== s.id;
  const n = setup.steps.length;
  $('#bar').style.width = `${Math.round((i / (n - 1)) * 100)}%`;
  $('#where').textContent = `Step ${i + 1} of ${n}`;
  $('#back').hidden = i === 0;
  $('#skip').hidden = !s.optional || s.done;
  $('#next').textContent = s.id === 'done' ? 'Finish' : s.id === 'introskipper' && !s.done ? 'Install' : 'Next';
  if (s.id === 'done') {
    const names = { tmdb: 'TMDB', introskipper: 'Intro Skipper', seerr: 'Seerr' };
    const later = [...skipped].map((id) => names[id]);
    $('#skipped').replaceChildren(...later.map((name) => {
      const li = document.createElement('li');
      li.textContent = `${name}: skipped, you can set it up later in the control panel.`;
      return li;
    }));
  }
  say('');
  updateButtons();
  const first = document.querySelector(`section[data-step="${s.id}"] input:not([readonly]), section[data-step="${s.id}"] select`);
  if (first && !first.disabled) first.focus();
}

function updateButtons() {
  const s = step();
  const confirming = s.id === 'jellyfin' && !$('#survey').hidden && !$('#dedicated').checked;
  $('#next').disabled = busy || confirming;
  $('#back').disabled = busy;
  $('#skip').disabled = busy;
}

function say(text, kind = '') {
  $('#status').className = `status ${kind}`;
  $('#status').textContent = text;
}

function fail(text) {
  say(text, 'error');
}

// post sends a step's answer with the code; it returns the answer, or null
// once it said what's wrong.
async function post(id, fields = {}) {
  let res;
  try {
    res = await fetch(`/admin/api/setup/${id}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: $('#code').value, ...fields }),
    });
  } catch {
    fail("Overfin didn't answer. Is it still running?");
    return null;
  }
  const body = res.status === 204 ? {} : await res.json().catch(() => ({}));
  if (res.ok) return body;
  if (res.status === 403 && id !== 'code') {
    // Overfin restarted, so its code changed: back to the first step.
    show(0);
  } else if (res.status === 409 && body.step) {
    const i = setup.steps.findIndex((s) => s.id === body.step);
    if (i >= 0) show(i);
  }
  fail(body.error || `Something went wrong (${res.status}).`);
  return null;
}

function note(id, text) {
  $(id).textContent = text;
  $(id).hidden = !text;
}

// The steps' Next: what each saves. It returns true to move on.
const actions = {
  code: async () => (await post('code')) !== null,
  language: async () => (await post('language', { language: $('#language').value })) !== null,
  jellyfin: async () => {
    const ans = await post('jellyfin', {
      url: $('#url').value, username: $('#jf-username').value, password: $('#jf-password').value,
      confirm: shown && $('#dedicated').checked ? shown : '',
    });
    if (!ans) return false;
    if (!ans.done) {
      jellyfinRound(ans);
      return false;
    }
    for (const id of ['#url', '#jf-username', '#jf-password', '#dedicated']) $(id).disabled = true;
    $('#handover').hidden = true;
    $('#survey').hidden = true;
    note('#jellyfin-done', ans.summary);
    return 'stay';
  },
  tmdb: async () => {
    if (!$('#tmdb-key').value.trim()) {
      fail('Paste the key, or skip this step.');
      return false;
    }
    const ans = await post('tmdb', { key: $('#tmdb-key').value });
    if (!ans) return false;
    $('#tmdb-key').value = '';
    $('#tmdb-key').disabled = true;
    note('#tmdb-done', ans.summary);
    return 'stay';
  },
  introskipper: async () => {
    let p = await post('introskipper', { start: true });
    while (p && p.running) {
      say(p.step || 'Installing…');
      await new Promise((r) => setTimeout(r, 2000));
      p = await post('introskipper');
    }
    if (!p) return false;
    if (p.error) {
      fail(p.error);
      return false;
    }
    note('#skipper-state', `Intro Skipper ${p.status.version} is installed and active.`);
    return 'stay';
  },
  seerr: async () => {
    const ans = await post('seerr', { url: $('#seerr-url').value, key: $('#seerr-key').value });
    if (!ans) return false;
    $('#seerr-key').value = '';
    for (const id of ['#seerr-url', '#seerr-key']) $(id).disabled = true;
    note('#seerr-done', ans.summary);
    return 'stay';
  },
  admin: async () => {
    if ((await post('admin', { email: $('#email').value, password: $('#password').value })) === null) return false;
    $('#password').value = '';
    for (const id of ['#email', '#password']) $(id).disabled = true;
    note('#admin-done', 'Your account is made.');
    return true;
  },
  done: async () => {
    if ((await post('finish')) === null) return false;
    $('#done-title').textContent = 'Done';
    $('#done-text').textContent = 'Overfin is set up.';
    $('#next-panel').hidden = setup.site;
    $('#next-site').hidden = !setup.site;
    document.querySelector('.wizard-foot').hidden = true;
    return false;
  },
};

// jellyfinRound shows what the server asks for next: its administrator's
// login, then what it holds.
function jellyfinRound(j) {
  const name = j.server.name ? `${j.server.name} (Jellyfin ${j.server.version})` : `This Jellyfin (${j.server.version})`;
  $('#found').textContent = name;
  $('#handover').hidden = false;
  if (j.survey) {
    shown = j.server.id;
    const users = j.survey.users || [];
    $('#survey-users').textContent = users.length === 1 ? '1 user' : `${users.length} users`;
    $('#survey-users').title = users.join(', ');
    $('#survey-libraries').textContent = j.survey.libraries === 1 ? '1 library' : `${j.survey.libraries} libraries`;
    $('#survey').hidden = false;
    $('#dedicated').checked = false;
    $('#next').textContent = 'Hand it over to Overfin';
    say('Check what it holds, then confirm.');
  } else {
    say('Sign in as its administrator.');
  }
}

// Changing the address or the login starts the Jellyfin step over.
function resetJellyfin() {
  shown = null;
  $('#survey').hidden = true;
  if (step().id === 'jellyfin') $('#next').textContent = 'Next';
  updateButtons();
}

async function next() {
  const s = step();
  if (busy || $('#next').disabled) return;
  if (s.done && s.id !== 'done') {
    show(at + 1);
    return;
  }
  busy = true;
  updateButtons();
  say(s.id === 'done' ? 'Finishing…' : 'Checking…');
  try {
    const result = await actions[s.id]();
    if (result) {
      s.done = s.id !== 'code' && s.id !== 'done';
      skipped.delete(s.id);
      if (result === 'stay') {
        // Its answer is worth reading: Next moves on.
        show(at);
      } else {
        show(at + 1);
      }
    }
  } finally {
    busy = false;
    updateButtons();
  }
}

function skip() {
  if (busy) return;
  skipped.add(step().id);
  show(at + 1);
}

async function copy() {
  const input = $('#command');
  try {
    await navigator.clipboard.writeText(input.value);
  } catch {
    // Not over HTTPS, the clipboard API isn't there: copy the selection.
    input.select();
    document.execCommand('copy');
  }
  $('#copy').textContent = 'Copied';
  setTimeout(() => { $('#copy').textContent = 'Copy'; }, 2000);
}

$('#next').addEventListener('click', next);
$('#back').addEventListener('click', () => { if (!busy && at > 0) show(at - 1); });
$('#skip').addEventListener('click', skip);
$('#copy').addEventListener('click', copy);
$('#dedicated').addEventListener('change', updateButtons);
for (const id of ['#url', '#jf-username', '#jf-password']) $(id).addEventListener('input', resetJellyfin);
for (const input of document.querySelectorAll('input:not([type=checkbox]):not([readonly])')) {
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') next(); });
}
load();
