// Overfin's first-run setup page: what's missing (Jellyfin's address and
// API key, the first admin), tested before it's saved. Text from the
// server is only ever set with textContent.

const $ = (sel) => document.querySelector(sel);
let asks = { jellyfin: true, admin: false, site: false };

async function load() {
  try {
    const res = await fetch('/admin/api/setup');
    if (res.ok) asks = await res.json();
  } catch {
    // The defaults: Jellyfin only.
  }
  $('#jellyfin').hidden = !asks.jellyfin;
  $('#admin').hidden = !asks.admin;
}

async function save() {
  const status = $('#status');
  const button = $('#save');
  status.className = 'muted';
  status.textContent = 'Testing…';
  button.disabled = true;
  try {
    const res = await fetch('/admin/api/setup', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        code: $('#code').value, url: $('#url').value, key: $('#key').value,
        email: $('#email').value, password: $('#password').value,
      }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      status.className = 'error';
      status.textContent = body.error || `Something went wrong (${res.status}).`;
      await load(); // what's still missing, after a partial save
      return;
    }
    $('#summary').textContent = body.summary;
    $('#form').hidden = true;
    $('#done').hidden = false;
    $('#next-panel').hidden = asks.site;
    $('#next-site').hidden = !asks.site;
  } catch {
    status.className = 'error';
    status.textContent = "Overfin didn't answer. Is it still running?";
  } finally {
    button.disabled = false;
  }
}

$('#save').addEventListener('click', save);
for (const input of document.querySelectorAll('input')) {
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') save(); });
}
load();
