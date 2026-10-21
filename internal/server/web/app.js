'use strict';
const $ = id => document.getElementById(id);
let apiToken = '';
let refreshPending = false;
async function api(path, method = 'GET', body) {
  const headers = {Authorization: 'Bearer ' + apiToken};
  if (body !== undefined) headers['Content-Type'] = 'application/json';
  const response = await fetch('/api/v1' + path, {method, headers, body: body === undefined ? undefined : JSON.stringify(body), cache: 'no-store', credentials: 'omit'});
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || 'Request failed');
  return data;
}
function status(message) { $('status').textContent = message; }
function secret(message) { $('new-token').textContent = message; $('dismiss').hidden = !message; }
function cell(row, text) { const el = document.createElement('td'); el.textContent = String(text); row.append(el); }
async function refresh() {
  if (!apiToken || refreshPending) return;
  refreshPending = true;
  try {
    const [machines, enrollments] = await Promise.all([api('/machines'), api('/enrollment-tokens')]);
    $('machines').replaceChildren();
    for (const m of machines) {
      const row = document.createElement('tr');
      for (const value of [m.display_id, m.name, m.online ? 'online' : 'offline', m.public_ip, m.created_at, m.last_seen]) cell(row, value);
      $('machines').append(row);
    }
    $('enrollments').replaceChildren();
    for (const e of enrollments) {
      const row = document.createElement('tr');
      for (const value of [e.id, e.expires_at, e.reusable, e.used, e.revoked]) cell(row, value);
      const action = document.createElement('td');
      const button = document.createElement('button');
      button.type = 'button'; button.textContent = 'Revoke'; button.disabled = e.revoked;
      button.addEventListener('click', () => execute(async () => { await api('/enrollment-tokens/' + encodeURIComponent(e.id), 'DELETE'); await refresh(); }));
      action.append(button); row.append(action); $('enrollments').append(row);
    }
    status(machines.length + ' machine(s). Inventory updated.');
  } finally { refreshPending = false; }
}
async function execute(work) { try { await work(); } catch (error) { status(error.message); } }
$('register').addEventListener('submit', event => {
  event.preventDefault(); execute(async () => {
    const out = await api('/accounts', 'POST', {name: $('name').value.trim()});
    apiToken = out.token.token;
    $('account').textContent = 'Logged in as ' + out.account.name;
    secret('API token, shown once. Save it privately. ' + apiToken);
    await refresh();
  });
});
$('login').addEventListener('submit', event => {
  event.preventDefault(); execute(async () => {
    apiToken = $('token').value.trim(); $('token').value = ''; secret('');
    try { const account = await api('/me'); $('account').textContent = 'Logged in as ' + account.name; await refresh(); }
    catch (error) { apiToken = ''; $('account').textContent = ''; throw error; }
  });
});
$('enroll').addEventListener('submit', event => {
  event.preventDefault(); execute(async () => {
    if (!apiToken) throw new Error('Log in first.');
    const out = await api('/enrollment-tokens', 'POST', {ttl_seconds: Number($('ttl').value) * 60, reusable: $('reusable').checked});
    secret('Installer URL, shown once. Treat it as a secret. ' + location.origin + '/script/' + out.token);
    await refresh();
  });
});
$('refresh').addEventListener('click', () => execute(refresh));
$('dismiss').addEventListener('click', () => secret(''));
$('logout').addEventListener('click', () => {
  apiToken = ''; secret(''); $('token').value = ''; $('account').textContent = '';
  $('machines').replaceChildren(); $('enrollments').replaceChildren(); status('Logged out.');
});
setInterval(() => execute(refresh), 30000);
