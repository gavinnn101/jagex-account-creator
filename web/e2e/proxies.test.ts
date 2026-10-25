import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import type { components } from '#lib/api.gen.d.ts';
import {
  acceptingServer,
  type AcceptingServer,
  closedPort,
  expectNoAxeViolations,
  openSections,
  readJSON,
  shownRows,
} from './helpers.ts';

type Proxy = components['schemas']['Proxy'];
type RunState = components['schemas']['RunState'];
type ProxyList = components['schemas']['ProxyList'];
type ProxyImport = components['schemas']['ProxyImport'];
type ProxyTest = components['schemas']['ProxyTest'];
type ErrorBody = components['schemas']['Error'];

// The tests change the one proxy list of serve, so they run in order.
test.describe.configure({ mode: 'serial' });

// An upstream that accepts each connection, so its test passes.
let upstream: AcceptingServer | undefined;
let livePort = 0;
// A port with no listener, so its test fails.
let deadPort = 0;

test.beforeAll(async () => {
  upstream = await acceptingServer();
  livePort = upstream.port;
  deadPort = await closedPort();
});

test.afterAll(async () => {
  await upstream?.close();
});

const live = () => `127.0.0.1:${String(livePort)}`;
const dead = () => `127.0.0.1:${String(deadPort)}`;
const socks = 'socks5://127.0.0.1:1';

// The import text in the form of the export: one line for each proxy, credentials included.
const fileText = () => `${live()}\n${dead()}:e2e:secret\n${socks}\n`;

async function listed(request: APIRequestContext): Promise<Proxy[]> {
  return (await readJSON<ProxyList>(request, '/api/proxies')).proxies;
}

// The header of the table as the screen shows the list. An optional column shows only when a
// listed proxy has its value, and Test follows Proxy. No run of these tests loads a proxy, so no
// proxy has a cooldown.
function headers(proxies: Proxy[], tested: boolean): string[] {
  return [
    'Proxy',
    ...(tested ? ['Test'] : []),
    ...(proxies.some((p) => p.attempts > 0) ? ['Attempts', 'Success rate', 'Bytes'] : []),
    ...(proxies.some((p) => p.last_attempt_at !== undefined) ? ['Last attempt'] : []),
    ...(proxies.some((p) => p.last_failure !== undefined) ? ['Last failure'] : []),
    'Actions',
  ];
}

// The cells of a row as the screen shows a proxy with no attempt: the whole address and its
// badges, the test cell after a test, and the actions, which are icons with no text.
function row(p: Proxy, test?: string): string[] {
  expect(p.attempts).toBe(0);
  expect(p.last_attempt_at).toBeUndefined();
  expect(p.last_failure).toBeUndefined();
  const proxy = [p.proxy, ...(p.auth ? ['Auth'] : []), ...(p.enabled ? [] : ['Disabled'])];
  return [proxy.join(' '), ...(test === undefined ? [] : [test]), ''];
}

function proxyTable(page: Page) {
  return page.getByRole('table', { name: 'Proxies' });
}

// Checks the summary line, the header and the rows against the list of the API. tests holds the
// test cell of each row when the rows show a test.
async function expectList(page: Page, proxies: Proxy[], tests?: string[]): Promise<void> {
  const enabled = proxies.filter((p) => p.enabled).length;
  const noun = proxies.length === 1 ? 'proxy' : 'proxies';
  const summary = `${String(proxies.length)} ${noun}, ${String(enabled)} enabled. A change applies at the next start of a run.`;
  await expect(page.getByText(summary, { exact: true })).toBeVisible();
  const run = await readJSON<RunState>(page.request, '/api/run');
  expect(run.cooldowns).toEqual([]);
  await expect(proxyTable(page).getByRole('columnheader')).toHaveText(
    headers(proxies, tests !== undefined),
  );
  await expect
    .poll(() => shownRows(proxyTable(page)))
    .toEqual(proxies.map((p, i) => row(p, tests?.[i])));
  // The screen can cut a long host short, so the title holds the whole host.
  const titles = await proxyTable(page)
    .getByRole('link')
    .evaluateAll((links) => links.map((a) => a.getAttribute('title')));
  expect(titles).toEqual(proxies.map((p) => p.proxy));
}

// Opens the Proxies screen and its import section, which starts closed when the list has proxies.
async function openProxies(page: Page) {
  await page.goto('/');
  await page
    .getByRole('navigation', { name: 'Main' })
    .getByRole('link', { name: 'Proxies' })
    .click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Proxies');
  await openSections(page);
  return page.getByRole('form', { name: 'Import proxies' });
}

function button(page: Page, name: string) {
  return page.getByRole('button', { name, exact: true });
}

test('a bad line names its line number and changes nothing', async ({ page, request }) => {
  const form = await openProxies(page);
  await expect(page.getByText('No proxies yet. Import a proxy file below.')).toBeVisible();
  await expect(proxyTable(page)).toHaveCount(0);

  await form.getByLabel('Proxy lines').fill(`${live()}\nnot a proxy:x:y:z:w\n`);
  const answer = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/proxies/import');
  await form.getByRole('button', { name: 'Import' }).click();
  const response = await answer;
  expect(response.status()).toBe(400);
  const { message } = (await response.json()) as ErrorBody;
  expect(message).toMatch(/^line 2: /);
  expect(message).not.toContain('not a proxy');
  await expect(form.getByRole('alert')).toHaveText(`Import failed: ${message}`);
  expect(await listed(request)).toEqual([]);

  await expectNoAxeViolations(page);
});

test('an import adds the proxies, and the export gives the same text', async ({
  page,
  request,
}) => {
  const form = await openProxies(page);
  await form.getByLabel('Proxy lines').fill(fileText());
  const answer = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/proxies/import');
  await form.getByRole('button', { name: 'Import' }).click();
  const response = await answer;
  expect(response.request().headers()['content-type']).toBe('text/plain');
  expect(response.status()).toBe(200);
  const counts = (await response.json()) as ProxyImport;
  expect(counts).toEqual({ added: 3, updated: 0, repeated: 0 });
  await expect(form.getByRole('status')).toHaveText('Added 3, updated 0, repeated 0.');

  const proxies = await listed(request);
  expect(proxies.map((p) => [p.proxy, p.auth, p.enabled])).toEqual([
    [live(), false, true],
    [dead(), true, true],
    [socks, false, true],
  ]);
  await expectList(page, proxies);
  await expect(page.getByRole('link', { name: live(), exact: true })).toHaveAttribute(
    'href',
    `/accounts?${new URLSearchParams({ proxy: live() }).toString()}`,
  );

  const download = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Export', exact: true }).click();
  expect((await download).suggestedFilename()).toBe('proxies.txt');
  const file = await request.get('/api/proxies/export');
  expect(file.status()).toBe(200);
  expect(await file.text()).toBe(fileText());

  await expectNoAxeViolations(page);
});

test('disable, enable and delete change the list', async ({ page, request }) => {
  await openProxies(page);

  await button(page, `Disable ${live()}`).click();
  await expect(button(page, `Enable ${live()}`)).toBeVisible();
  let proxies = await listed(request);
  expect(proxies[0]?.enabled).toBe(false);
  await expectList(page, proxies);
  await expectNoAxeViolations(page);

  await button(page, `Enable ${live()}`).click();
  await expect(button(page, `Disable ${live()}`)).toBeVisible();
  proxies = await listed(request);
  expect(proxies[0]?.enabled).toBe(true);
  await expectList(page, proxies);

  await button(page, `Delete ${socks}`).click();
  await expectNoAxeViolations(page);
  await button(page, `Confirm delete ${socks}`).click();
  await expect(page.getByRole('link', { name: socks, exact: true })).toHaveCount(0);
  proxies = await listed(request);
  expect(proxies.map((p) => p.proxy)).toEqual([live(), dead()]);
  await expectList(page, proxies);

  await expectNoAxeViolations(page);
});

// Clicks the test of one proxy and returns the answer.
async function clickTest(page: Page, proxy: string): Promise<ProxyTest> {
  const answer = page.waitForResponse((r) => new URL(r.url()).pathname.endsWith('/test'));
  await button(page, `Test ${proxy}`).click();
  const response = await answer;
  expect(response.status()).toBe(200);
  return (await response.json()) as ProxyTest;
}

test('a test passes on a listener and fails on a closed port', async ({ page, request }) => {
  await openProxies(page);

  const ok = await clickTest(page, live());
  expect(ok.ok).toBe(true);
  const failed = await clickTest(page, dead());
  expect(failed.ok).toBe(false);
  expect(failed.error).toContain(dead());
  expect(failed.error).not.toContain('secret');

  const proxies = await listed(request);
  expect(proxies.map((p) => p.proxy)).toEqual([live(), dead()]);
  // A failed test shows its badge, then the error.
  await expectList(page, proxies, [
    `OK in ${String(ok.duration_ms)} ms`,
    `Failed in ${String(failed.duration_ms)} ms ${failed.error ?? ''}`,
  ]);

  await expectNoAxeViolations(page);
});
