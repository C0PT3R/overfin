// The migration module's tab: accounts brought over but still linked to
// their old login, and moving them for good once the old way is closed.
export default async function render(root, panel) {
  const { h, api } = panel;
  const msg = h('p', { class: 'muted' });
  const show = ({ linked, mode }) => {
    const move = linked > 0 && h('button', { class: 'danger', onclick: async () => {
      if (!confirm(`Move ${linked} linked account${linked === 1 ? '' : 's'} for good? Their old logins stop working.`)) return;
      move.disabled = true;
      msg.textContent = 'Moving…';
      try {
        const out = await api('POST', '/linked/move', {});
        show({ linked: out.linked, mode });
        msg.textContent = out.linked > 0
          ? `${out.moved} moved; ${out.linked} could not be, see the logs, then try again.`
          : `${out.moved} moved.`;
      } catch (err) {
        move.disabled = false;
        msg.textContent = err.message;
      }
    } }, 'Move linked accounts');
    root.replaceChildren(h('section', {},
      h('h2', {}, 'Migration'),
      h('p', {}, mode === 'link'
        ? 'Accounts brought over stay linked: their old login keeps working.'
        : 'Accounts brought over are moved: their old login stops working at once.'),
      h('p', {}, linked === 0
        ? 'No account is linked.'
        : `${linked} account${linked === 1 ? ' is' : 's are'} linked. Once you close the old way in, move them for good.`),
      move, msg));
  };
  show(await api('GET', '/linked'));
}
