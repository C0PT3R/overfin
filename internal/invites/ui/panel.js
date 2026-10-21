// The invites module's tab: invitations, and the invite templates they're
// made from. Text from the server is only ever set with textContent.
export default async function render(root, panel) {
  const { h, api } = panel;
  const [{ items: templates }, { items: invites }, { items: accountTemplates }] = await Promise.all([
    api('GET', '/templates'), api('GET', '/invites'), api('GET', '/account-templates')]);
  const reload = () => render(root, panel);
  const accountName = (id) => accountTemplates.find((t) => t.id === id)?.name || 'a deleted template';
  const days = (n) => `${n} day${n === 1 ? '' : 's'}`;
  const field = (text, input) => h('label', { class: 'field' }, h('span', {}, text), input);
  const failed = (msg) => (err) => { msg.className = 'error'; msg.textContent = err.message; };

  // Invitations.
  const status = (inv) => (inv.uses >= inv.maxUses ? 'used' : new Date(inv.expiresAt) <= new Date() ? 'expired' : 'active');
  const inviteMsg = h('p', {});
  const forWhom = h('input', { maxlength: '64', placeholder: 'e.g. Bob' });
  const from = h('select', {}, templates.map((t) => h('option', { value: t.id }, t.name)));
  const made = h('div', {});
  const create = h('button', { class: 'primary', onclick: async () => {
    inviteMsg.textContent = '';
    try {
      const out = await api('POST', '/invites', { template: from.value, label: forWhom.value.trim() });
      const link = h('input', { value: out.link, readonly: true });
      made.replaceChildren(h('div', { class: 'notice' },
        h('p', {}, 'Send this link to ', out.invite.label || 'them', '. It is shown only now: copy it before leaving this page.'),
        link,
        h('button', { onclick: () => navigator.clipboard.writeText(out.link).then(() => { inviteMsg.className = ''; inviteMsg.textContent = 'Copied.'; }) }, 'Copy'),
        out.link.startsWith('/') && h('p', { class: 'hint' }, "Put your site's address in front of it.")));
      link.select();
      invites.unshift(out.invite);
      drawInvites();
    } catch (err) {
      failed(inviteMsg)(err);
    }
  } }, 'Make an invitation');
  const inviteRows = h('tbody', {});
  const drawInvites = () => inviteRows.replaceChildren(...(invites.length ? invites.map((inv) => h('tr', {},
    h('td', {}, inv.label || h('span', { class: 'muted' }, 'nobody in particular')),
    h('td', {}, h('span', { class: `pill ${status(inv) === 'active' ? 'active' : 'disabled'}` }, status(inv))),
    h('td', {}, `${inv.uses} / ${inv.maxUses}`),
    h('td', { class: 'hint' }, `${accountName(inv.accountTemplate)}, ${inv.accessDays ? days(inv.accessDays) : 'no end'}`),
    h('td', { class: 'hint' }, new Date(inv.expiresAt).toLocaleString()),
    h('td', {}, h('button', { onclick: () => {
      if (!confirm('Delete this invitation? Its link stops working; accounts made with it stay.')) return;
      api('DELETE', `/invites/${inv.id}`).then(reload, failed(inviteMsg));
    } }, 'Delete')))) : [h('tr', {}, h('td', { colspan: '6', class: 'muted' }, 'No invitations yet.'))]));
  drawInvites();

  // Invite templates.
  const tmplMsg = h('p', {});
  const name = h('input', { maxlength: '64', placeholder: 'e.g. Friends' });
  const account = h('select', {}, accountTemplates.map((t) => h('option', { value: t.id },
    `${t.name} (${t.maxStreams} stream${t.maxStreams > 1 ? 's' : ''}, ${t.maxProfiles} profile${t.maxProfiles > 1 ? 's' : ''})`)));
  const access = h('input', { type: 'number', min: '0', max: '3650', value: '0' });
  const valid = h('input', { type: 'number', min: '1', max: '365', value: '7' });
  const uses = h('input', { type: 'number', min: '1', max: '1000', value: '1' });
  let editing = null;
  const save = h('button', { class: 'primary', onclick: () => {
    const body = { name: name.value.trim(), accountTemplate: account.value,
      accessDays: Number(access.value), validDays: Number(valid.value), maxUses: Number(uses.value) };
    (editing ? api('PUT', `/templates/${editing}`, body) : api('POST', '/templates', body)).then(reload, failed(tmplMsg));
  } }, 'Add');
  const edit = (t) => {
    editing = t.id;
    name.value = t.name;
    account.value = t.accountTemplate;
    access.value = t.accessDays;
    valid.value = t.validDays;
    uses.value = t.maxUses;
    save.textContent = 'Save';
  };
  const tmplRows = templates.length ? templates.map((t) => h('tr', {},
    h('td', {}, t.name),
    h('td', { class: 'hint' }, accountName(t.accountTemplate)),
    h('td', { class: 'hint' }, t.accessDays ? days(t.accessDays) : 'no end'),
    h('td', { class: 'hint' }, `link works ${days(t.validDays)}, ${t.maxUses} use${t.maxUses === 1 ? '' : 's'}`),
    h('td', {}, h('button', { onclick: () => edit(t) }, 'Edit'), ' ',
      h('button', { onclick: () => {
        if (!confirm(`Delete the template ${t.name}? Invitations made from it stay as they are.`)) return;
        api('DELETE', `/templates/${t.id}`).then(reload, failed(tmplMsg));
      } }, 'Delete')))) : [h('tr', {}, h('td', { colspan: '5', class: 'muted' }, 'No invite templates yet.'))];

  root.replaceChildren(
    h('section', {}, h('h2', {}, 'Invitations'),
      templates.length
        ? h('div', { class: 'toolbar' }, field('For', forWhom), field('From', from), create)
        : h('p', { class: 'muted' }, 'Make an invite template first, below.'),
      made, inviteMsg,
      h('table', {}, h('thead', {}, h('tr', {}, ...['For', 'Status', 'Used', 'Gives', 'Link works until', ''].map((t) => h('th', {}, t)))),
        inviteRows)),
    h('section', {}, h('h2', {}, 'Invite templates'),
      h('p', { class: 'hint' }, 'What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.'),
      h('table', {}, h('tbody', {}, ...tmplRows)),
      h('div', {},
        field('Name', name), field('Account template', account),
        field('Access lasts (days, 0: no end)', access), field('Link works (days)', valid), field('Uses', uses),
        h('div', { class: 'toolbar' }, save, tmplMsg))));
}
