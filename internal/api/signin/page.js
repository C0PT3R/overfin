// The sign-in page: each form posts JSON to Overfin and follows its answer.
'use strict';

const page = document.body.dataset;
const fresh = page.fresh === '1';
const $ = (id) => document.getElementById(id);
let email = '';

async function post(path, data) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...data, fresh, return: page.return }),
  });
  let answer = {};
  try {
    answer = await res.json();
  } catch {
    // No JSON: the generic message below.
  }
  if (!res.ok) throw new Error(answer.detail || page.failed);
  return answer;
}

function show(id) {
  for (const form of document.forms) form.hidden = form.id !== id;
  $('error').textContent = '';
  $(id).querySelector('input')?.focus();
}

// run sends one form at a time, showing what went wrong.
function run(form, action) {
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const buttons = form.querySelectorAll('button');
    for (const b of buttons) b.disabled = true;
    $('error').textContent = '';
    try {
      const answer = await action();
      if (answer?.next) location.assign(answer.next);
    } catch (err) {
      $('error').textContent = err.message;
    } finally {
      for (const b of buttons) b.disabled = false;
    }
  });
}

async function askCode() {
  await post('/api/v1/signin', { email });
  $('sent').textContent = page.codeSent.replace('{email}', fresh ? page.email : email);
  $('code-input').value = '';
  show('code');
}

run($('start'), async () => {
  email = fresh ? '' : $('email').value.trim();
  const password = $('password').value;
  if (password === '') return askCode();
  return post('/api/v1/signin', { email, password });
});

run($('code'), () => post('/api/v1/signin/code', { email, code: $('code-input').value }));

$('new-code').addEventListener('click', () => askCode().catch((err) => { $('error').textContent = err.message; }));
$('other-email')?.addEventListener('click', () => show('start'));

// A link from an email: its token stays in the fragment, which never
// reaches a server's logs, and is used only on a click, so mail scanners
// opening the link use nothing.
const link = new URLSearchParams(location.hash.slice(1)).get('link');
if (link) {
  history.replaceState(null, '', location.pathname + location.search);
  show('link');
  run($('link'), () => post('/api/v1/signin/link', { link }));
}
