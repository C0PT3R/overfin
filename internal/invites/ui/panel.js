// The invites module's tab: invitations, and the invite templates they're
// made from. Text from the server is only ever set with textContent.
export default async function render(root, panel) {
  const { h, api } = panel;
  const { card, field, badge, toast, busy, secret, fill, plural } = panel.ui;
  const [{ items: templates }, { items: invites }, { items: accountTemplates }] = await Promise.all([
    api('GET', '/templates'), api('GET', '/invites'), api('GET', '/account-templates')]);
  const reload = () => render(root, panel);
  const accountName = (id) => accountTemplates.find((t) => t.id === id)?.name || 'a deleted template';
  const days = (n) => plural(n, 'day');

  // Invitations.
  const status = (inv) => (inv.uses >= inv.maxUses ? ['Used', ''] : new Date(inv.expiresAt) <= new Date() ? ['Expired', 'err'] : ['Waiting', 'ok']);
  const forWhom = h('input', { maxlength: '64', placeholder: 'e.g. Bob' });
  const from = h('select', {}, templates.map((t) => h('option', { value: t.id }, t.name)));
  const made = h('div', {});
  const create = h('button', { class: 'primary', type: 'submit' }, 'Make an invitation');
  const make = (e) => {
    e.preventDefault();
    busy(create, async () => {
      const out = await api('POST', '/invites', { template: from.value, label: forWhom.value.trim() });
      const link = out.link.startsWith('/') ? location.origin.replace(/:\d+$/, '') + out.link : out.link;
      fill(made, secret(`Send this link to ${out.invite.label || 'them'}. It's shown only now: copy it before leaving this page.`, link,
        out.link.startsWith('/') ? 'Check the address in front: it must be your site\'s.' : null));
      forWhom.value = '';
      invites.unshift(out.invite);
      drawInvites();
    });
  };
  const inviteRows = h('tbody', {});
  const drawInvites = () => fill(inviteRows, invites.length ? invites.map((inv) => {
    const [text, kind] = status(inv);
    const remove = h('button', { type: 'button', class: 'small', onclick: () => {
      if (!confirm('Delete this invitation? Its link stops working; accounts made with it stay.')) return;
      busy(remove, async () => { await api('DELETE', `/invites/${inv.id}`); toast('Invitation deleted.'); reload(); });
    } }, 'Delete');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, inv.label || 'Anyone with the link'), h('div', { class: 'hint' }, `${inv.uses} of ${plural(inv.maxUses, 'use')}`)),
      h('td', {}, badge(text, kind)),
      h('td', { class: 'hint' }, `${accountName(inv.accountTemplate)}, ${inv.accessDays ? days(inv.accessDays) : 'no end'}`),
      h('td', { class: 'hint' }, new Date(inv.expiresAt).toLocaleDateString()),
      h('td', { class: 'actions' }, remove));
  }) : h('tr', {}, h('td', { colspan: '5', class: 'empty' }, 'No invitations yet.')));
  drawInvites();

  // Invite templates.
  const name = h('input', { maxlength: '64', placeholder: 'e.g. Friends' });
  const account = h('select', {}, accountTemplates.map((t) => h('option', { value: t.id },
    `${t.name} · ${plural(t.maxStreams, 'stream')}, ${plural(t.maxProfiles, 'profile')}`)));
  const access = h('input', { type: 'number', min: '0', max: '3650', value: '0' });
  const valid = h('input', { type: 'number', min: '1', max: '365', value: '7' });
  const uses = h('input', { type: 'number', min: '1', max: '1000', value: '1' });
  let editing = null;
  const title = h('h2', {}, 'A new invite template');
  const save = h('button', { class: 'primary', type: 'button', onclick: () => busy(save, async () => {
    const body = { name: name.value.trim(), accountTemplate: account.value,
      accessDays: Number(access.value), validDays: Number(valid.value), maxUses: Number(uses.value) };
    await (editing ? api('PUT', `/templates/${editing}`, body) : api('POST', '/templates', body));
    toast(editing ? 'Invite template saved.' : 'Invite template made.');
    reload();
  }) }, 'Make it');
  const cancel = h('button', { class: 'ghost', type: 'button', hidden: true, onclick: reload }, 'Cancel');
  const edit = (t) => {
    editing = t.id;
    name.value = t.name;
    account.value = t.accountTemplate;
    access.value = t.accessDays;
    valid.value = t.validDays;
    uses.value = t.maxUses;
    title.textContent = `Editing ${t.name}`;
    save.textContent = 'Save';
    cancel.hidden = false;
    name.focus();
  };
  const tmplRows = templates.length ? templates.map((t) => {
    const remove = h('button', { type: 'button', class: 'small', onclick: () => {
      if (!confirm(`Delete the invite template ${t.name}? Invitations made from it stay as they are.`)) return;
      busy(remove, async () => { await api('DELETE', `/templates/${t.id}`); toast('Invite template deleted.'); reload(); });
    } }, 'Delete');
    return h('tr', {},
      h('td', {}, h('div', { class: 'name' }, t.name), h('div', { class: 'hint' }, `Account: ${accountName(t.accountTemplate)}`)),
      h('td', { class: 'hint' }, t.accessDays ? days(t.accessDays) : 'No end'),
      h('td', { class: 'hint' }, `${days(t.validDays)}, ${plural(t.maxUses, 'use')}`),
      h('td', { class: 'actions' }, h('button', { type: 'button', class: 'small', onclick: () => edit(t) }, 'Edit'), remove));
  }) : [h('tr', {}, h('td', { colspan: '4', class: 'empty' }, 'No invite templates yet: make one below.'))];

  fill(root,
    card({ title: 'Invitations', description: 'A link that lets someone set up an account here, shaped by an invite template.' }, [
      templates.length
        ? h('form', { class: 'toolbar', onsubmit: make }, field('For', forWhom), field('From', from), create)
        : h('p', { class: 'notice' }, 'Make an invite template first, below.'),
      made,
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'For'), h('th', {}, 'Status'), h('th', {}, 'Gives'), h('th', {}, 'Link works until'), h('th', {}))),
        inviteRows)),
    ]),
    card({ title: 'Invite templates', description: 'What an invitation gives: an account template, how long access lasts, how long the link works and how many people can use it.' },
      h('div', { class: 'table-wrap' }, h('table', {},
        h('thead', {}, h('tr', {}, h('th', {}, 'Name'), h('th', {}, 'Access lasts'), h('th', {}, 'Link works'), h('th', {}))),
        h('tbody', {}, tmplRows)))),
    h('section', {},
      h('div', { class: 'card-head' }, h('div', {}, title)),
      h('div', { class: 'fields' },
        field('Name', name, null, true), field('Account template', account, 'What the account may do.', true),
        field('Access lasts', access, 'In days; 0: no end.'), field('Link works', valid, 'In days.'), field('Uses', uses, 'How many people.')),
      h('div', { class: 'card-foot' }, h('span', { class: 'grow' }), cancel, save)));
}
