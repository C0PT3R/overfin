// The migration module's tab: accounts brought over but still linked to
// their old login, and moving them for good once the old way is closed.
export default async function render(root, panel) {
  const { h, api } = panel;
  const { card, badge, toast, busy, status, fill, plural } = panel.ui;
  const show = ({ linked, mode }) => {
    const st = status(linked === 0
      ? 'No account is linked.'
      : `${plural(linked, 'account is', 'accounts are')} linked. Once you close the old way in, move them for good.`);
    const move = linked > 0 && h('button', { class: 'danger', type: 'button', onclick: () => {
      if (!confirm(`Move ${plural(linked, 'linked account')} for good? Their old logins stop working.`)) return;
      busy(move, async () => {
        st.set('Moving…');
        const out = await api('POST', '/linked/move', {});
        show({ linked: out.linked, mode });
        if (out.linked > 0) toast(`${out.moved} moved; ${out.linked} could not be: the log says why. Try again.`, true);
        else toast(`${plural(out.moved, 'account')} moved.`);
      }, (err) => st.set(err.message, 'error'));
    } }, 'Move linked accounts');
    fill(root, card({
      title: 'Bringing accounts over',
      badge: mode === 'link' ? badge('Link mode', 'warn') : badge('Move mode', 'info'),
      description: mode === 'link'
        ? 'Accounts brought over stay linked: their old login keeps working until you move them.'
        : 'Accounts brought over are moved: their old login stops working at once.',
    }, null, [st, move]));
  };
  show(await api('GET', '/linked'));
}
