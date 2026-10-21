// Overfin's first-run setup page: Jellyfin's address and API key, tested
// before they're saved. Text from the server is only ever set with
// textContent.

const $ = (sel) => document.querySelector(sel);

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
      body: JSON.stringify({ code: $('#code').value, url: $('#url').value, key: $('#key').value }),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      status.className = 'error';
      status.textContent = body.error || `Something went wrong (${res.status}).`;
      return;
    }
    $('#summary').textContent = body.summary;
    $('#form').hidden = true;
    $('#done').hidden = false;
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
