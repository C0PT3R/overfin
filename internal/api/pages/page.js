// Overfin's own pages: signing in, an invitation, a forgotten password.
// Each form posts JSON to Overfin and follows its answer. Forms that
// anyone can send first solve the server's check against scripts (a
// SHA-256 starting with enough zero bits). Links from emails keep their
// token in the fragment, which never reaches a server's logs, and are used
// only on a click, so mail scanners opening a link use nothing.
'use strict';

const page = document.body.dataset;
const $ = (id) => document.getElementById(id);

async function post(path, data) {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...data, return: page.return }),
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

// ---- The check against scripts ---------------------------------------------------

// sha256 is SHA-256 (FIPS 180-4) of a string's UTF-8 bytes; the first
// 32-bit word of its hash is all the check needs.
const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
  0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
  0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
  0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
  0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
  0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);
const W = new Uint32Array(64);
const encoder = new TextEncoder();

function firstWord(text) {
  const bytes = encoder.encode(text);
  const blocks = Math.ceil((bytes.length + 9) / 64);
  const msg = new Uint8Array(blocks * 64);
  msg.set(bytes);
  msg[bytes.length] = 0x80;
  const bitLen = bytes.length * 8;
  const view = new DataView(msg.buffer);
  view.setUint32(msg.length - 4, bitLen >>> 0);
  view.setUint32(msg.length - 8, Math.floor(bitLen / 0x100000000));
  let h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
  let h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;
  const rotr = (x, n) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < msg.length; off += 64) {
    for (let i = 0; i < 16; i++) W[i] = view.getUint32(off + i * 4);
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(W[i - 15], 7) ^ rotr(W[i - 15], 18) ^ (W[i - 15] >>> 3);
      const s1 = rotr(W[i - 2], 17) ^ rotr(W[i - 2], 19) ^ (W[i - 2] >>> 10);
      W[i] = (W[i - 16] + s0 + W[i - 7] + s1) >>> 0;
    }
    let a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;
    for (let i = 0; i < 64; i++) {
      const t1 = (h + (rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25)) + ((e & f) ^ (~e & g)) + K[i] + W[i]) >>> 0;
      const t2 = ((rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0;
      h = g; g = f; f = e; e = (d + t1) >>> 0; d = c; c = b; b = a; a = (t1 + t2) >>> 0;
    }
    h0 = (h0 + a) >>> 0; h1 = (h1 + b) >>> 0; h2 = (h2 + c) >>> 0; h3 = (h3 + d) >>> 0;
    h4 = (h4 + e) >>> 0; h5 = (h5 + f) >>> 0; h6 = (h6 + g) >>> 0; h7 = (h7 + h) >>> 0;
  }
  return h0;
}

// solve gets a check from the server and finds its answer, a little at a
// time, so the page stays responsive.
async function solve() {
  const res = await fetch('/api/v1/pages/challenge');
  if (!res.ok) throw new Error(page.failed);
  const { challenge, bits } = await res.json();
  const limit = 2 ** (32 - bits);
  for (let n = 0; ; ) {
    const stop = n + 5000;
    for (; n < stop; n++) {
      if (firstWord(challenge + ':' + n) < limit) return { challenge, answer: String(n) };
    }
    await new Promise((r) => setTimeout(r, 0));
  }
}

// ---- Forms ---------------------------------------------------------------------------

// run sends one form at a time, showing what went wrong.
function run(form, action) {
  if (!form) return;
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

// checked sends a form anyone can send, with its check solved.
async function checked(path, data) {
  const status = $('error');
  status.textContent = page.checking;
  const proof = await solve();
  status.textContent = '';
  return post(path, { ...data, ...proof });
}

function same(a, b) {
  if ($(a).value !== $(b).value) throw new Error(page.differ);
  return $(a).value;
}

function done(text) {
  for (const form of document.forms) form.hidden = true;
  $('done').textContent = text;
  $('done').hidden = false;
}

// Signing in; a person still to confirm their address can get the email
// again.
let email = '';
run($('signin'), async () => {
  email = $('email').value.trim();
  const answer = await checked('/api/v1/signin', { email, password: $('password').value });
  if (answer.unconfirmed) {
    $('unconfirmed').hidden = false;
    return null;
  }
  return answer;
});
$('resend')?.addEventListener('click', async () => {
  try {
    await checked('/api/v1/signin/resend', { email });
    $('unconfirmed').textContent = page.resent;
  } catch (err) {
    $('error').textContent = err.message;
  }
});

run($('fresh'), () => post('/api/v1/signin', { password: $('password').value, fresh: true }));
run($('change'), () => post('/api/v1/signin/password', { password: same('password', 'password2') }));

run($('register'), async () => {
  const password = same('password', 'password2');
  const to = $('email').value.trim();
  await checked('/api/v1/register', { email: to, password });
  done(page.registered.replace('{email}', to));
  return null;
});

run($('reset'), async () => {
  const to = $('email').value.trim();
  await checked('/api/v1/reset', { email: to });
  done(page.resetSent.replace('{email}', to));
  return null;
});

// Links from emails: confirming an address, choosing a new password.
const hash = new URLSearchParams(location.hash.slice(1));
const verify = hash.get('verify');
const reset = hash.get('reset');
if (verify || reset) history.replaceState(null, '', location.pathname + location.search);
if (verify && $('verify')) {
  $('signin').hidden = true;
  $('verify').hidden = false;
  run($('verify'), () => post('/api/v1/signin/verify', { link: verify }));
}
if (reset && $('reset-new')) {
  $('reset').hidden = true;
  $('reset-new').hidden = false;
  $('new-password').focus();
  run($('reset-new'), () => post('/api/v1/reset/password', { link: reset, password: same('new-password', 'new-password2') }));
}
