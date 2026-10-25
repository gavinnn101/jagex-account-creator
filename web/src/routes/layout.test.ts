import { createRawSnippet } from 'svelte';
import { afterEach, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import {
  FakeEventSource,
  lastSource,
  runState,
  stubEventSource,
} from '#lib/streamtest/streamtest.ts';
import Layout from './+layout.svelte';
import { prerender, ssr } from './+layout';

afterEach(() => {
  vi.unstubAllGlobals();
});

async function renderLayout() {
  stubEventSource();
  const children = createRawSnippet(() => ({ render: () => '<p>page body</p>' }));
  const screen = await render(Layout, { children });
  return { screen, source: lastSource() };
}

it('renders the app only in the browser', () => {
  expect(ssr).toBe(false);
  expect(prerender).toBe(false);
});

it('renders the page inside the main landmark', async () => {
  const { screen } = await renderLayout();
  await expect.element(screen.getByRole('main')).toHaveTextContent('page body');
  expect(document.title).toBe('Jagex account creator');
  const banner = screen.getByRole('banner');
  await expect.element(banner.getByText('Jagex account creator', { exact: true })).toBeVisible();
});

it('links each screen from the main navigation', async () => {
  const { screen } = await renderLayout();
  const nav = screen.getByRole('navigation', { name: 'Main' });
  await expect.element(nav).toBeVisible();
  const links = nav.getByRole('link').elements();
  expect(links.map((a) => a.textContent)).toEqual([
    'Run',
    'Logs',
    'Runs',
    'Accounts',
    'Proxies',
    'Checks',
  ]);
});

it('shows the run phase of the one stream in the header', async () => {
  const { screen, source } = await renderLayout();
  expect(FakeEventSource.count).toBe(1);
  expect(source.url).toBe('/api/events');
  const badge = screen.getByRole('banner').getByRole('link', { name: /^Run phase:/ });
  await expect.element(badge).toHaveAccessibleName('Run phase: Connecting');
  source.emit(runState('running'));
  await expect.element(badge).toHaveAccessibleName('Run phase: Running');
  source.fail();
  await expect.element(badge).toHaveAccessibleName('Run phase: Reconnecting');
});

it('has the theme toggle in the header', async () => {
  const { screen } = await renderLayout();
  const toggle = screen.getByRole('banner').getByRole('button', { name: 'Light theme' });
  await expect.element(toggle).toBeVisible();
});

it('closes the stream on destroy', async () => {
  const { screen, source } = await renderLayout();
  expect(source.closed).toBe(false);
  await screen.unmount();
  expect(source.closed).toBe(true);
});
