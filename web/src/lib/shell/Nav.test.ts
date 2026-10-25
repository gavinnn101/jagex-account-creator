import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import Nav from './Nav.svelte';

async function renderNav(pathname: string) {
  const screen = await render(Nav, { pathname });
  const nav = screen.getByRole('navigation', { name: 'Main' });
  await expect.element(nav).toBeVisible();
  return nav.getByRole('link').elements();
}

it('links each screen in order', async () => {
  const links = await renderNav('/');
  expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
    ['Run', '/'],
    ['Logs', '/logs'],
    ['Runs', '/runs'],
    ['Accounts', '/accounts'],
    ['Proxies', '/proxies'],
    ['Checks', '/checks'],
  ]);
});

it.for<[string, string]>([
  ['/', 'Run'],
  ['/logs', 'Logs'],
  ['/runs', 'Runs'],
  ['/runs/5', 'Runs'],
  ['/accounts/12', 'Accounts'],
  ['/proxies', 'Proxies'],
  ['/checks', 'Checks'],
])('marks the link of %s as the current page', async ([pathname, label]) => {
  const links = await renderNav(pathname);
  const current = links.filter((a) => a.getAttribute('aria-current') === 'page');
  expect(current.map((a) => a.textContent)).toEqual([label]);
});

it.for(['', '/runsx', '/unknown'])('marks no link at %j', async (pathname) => {
  const links = await renderNav(pathname);
  expect(links.filter((a) => a.hasAttribute('aria-current'))).toEqual([]);
});
