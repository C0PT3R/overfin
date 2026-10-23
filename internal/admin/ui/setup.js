// Overfin's first-run setup page: what's missing (Overfin's own Jellyfin,
// the first admin), checked before it's saved. A Jellyfin someone else set
// up takes two more steps: its administrator's login, then a confirmation
// once the page shows what it holds. Text from the server is only ever set
// with textContent.

const $ = (sel) => document.querySelector(sel);
let asks = { jellyfin: true, fixedUrl: '', admin: false, site: false };
// The server shown for confirmation, once its administrator signed in.
let shown = null;

async function load() {
  try {
    const res = await fetch('/admin/api/setup');
    if (res.ok) asks = await res.json();
  } catch {
    // The defaults: Jellyfin only.
  }
  $('#jellyfin').hidden = !asks.jellyfin;
  $('#admin').hidden = !asks.admin;
  $('#url-field').hidden = !!asks.fixedUrl;
  $('#fixed').hidden = !asks.fixedUrl;
  $('#fixed').textContent = asks.fixedUrl ? `At ${asks.fixedUrl}, as Overfin's configuration file says.` : '';
}

// jellyfinStep shows what the server asks for next.
function jellyfinStep(j) {
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
    $('#save').textContent = 'Hand it over to Overfin';
  }
  updateButton();
}

// While the survey shows, the button waits for the confirmation.
function updateButton() {
  $('#save').disabled = !$('#survey').hidden && !$('#dedicated').checked;
}

// Changing the address or the login starts the Jellyfin step over.
function reset() {
  shown = null;
  $('#survey').hidden = true;
  $('#save').textContent = 'Continue';
  updateButton();
}

async function save() {
  const status = $('#status');
  const button = $('#save');
  if (button.disabled) return;
  status.className = 'status';
  status.textContent = 'Checking…';
  button.disabled = true;
  try {
    const res = await fetch('/admin/api/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        code: $('#code').value, url: $('#url').value,
        jellyfinUsername: $('#jf-username').value, jellyfinPassword: $('#jf-password').value,
        confirm: shown && $('#dedicated').checked ? shown : '',
        email: $('#email').value, password: $('#password').value,
      }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      status.className = 'status error';
      status.textContent = body.error || `Something went wrong (${res.status}).`;
      await load(); // what's still missing, after a partial save
      return;
    }
    if (body.jellyfin) {
      status.textContent = body.jellyfin.survey ? 'Check what it holds, then confirm.' : 'Sign in as its administrator.';
      jellyfinStep(body.jellyfin);
      return;
    }
    $('#summary').textContent = body.summary;
    $('#form').hidden = true;
    $('#done').hidden = false;
    $('#next-panel').hidden = asks.site;
    $('#next-site').hidden = !asks.site;
  } catch {
    status.className = 'status error';
    status.textContent = "Overfin didn't answer. Is it still running?";
  } finally {
    updateButton();
  }
}

$('#save').addEventListener('click', save);
$('#dedicated').addEventListener('change', updateButton);
for (const id of ['#url', '#jf-username', '#jf-password']) $(id).addEventListener('input', reset);
for (const input of document.querySelectorAll('input:not([type=checkbox])')) {
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') save(); });
}
load();
