// The hello module's tab in the control panel: how many profiles said hello,
// and how often. It only uses the module's own admin route.
export default async function render(root, panel) {
  const { profiles, visits } = await panel.api('GET', '/stats');
  root.replaceChildren(panel.h('section', {},
    panel.h('h2', {}, 'Hello'),
    panel.h('p', {}, `${profiles} profile${profiles === 1 ? '' : 's'} said hello, ${visits} time${visits === 1 ? '' : 's'} in all.`)));
}
