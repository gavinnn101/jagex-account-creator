import { expect, test, type Page } from '@playwright/test';
import type { components } from '#lib/api.gen.d.ts';
import { expectNoAxeViolations, openSections, readJSON, shownRows } from './helpers.ts';

type Account = components['schemas']['Account'];
type AccountPage = components['schemas']['AccountPage'];
type AccountSummary = components['schemas']['AccountSummary'];

// The tests read and change the accounts that e2e/accounts.jsonl seeds, so they run in order.
test.describe.configure({ mode: 'serial' });

// The list shows a Run or a Proxy column only when an account of the page has the field.
function optionalColumns(accounts: AccountSummary[]): { run: boolean; proxy: boolean } {
  return {
    run: accounts.some((a) => a.run_id !== undefined),
    proxy: accounts.some((a) => a.proxy !== undefined),
  };
}

function listColumns(accounts: AccountSummary[]): string[] {
  const shown = optionalColumns(accounts);
  return [
    'ID',
    'Created',
    'Email',
    'Username',
    ...(shown.run ? ['Run'] : []),
    ...(shown.proxy ? ['Proxy'] : []),
    '2FA',
    'Status',
  ];
}

// The cells of each list row as the screen shows the summaries. The Created cell is a time
// element, and shownRows gives its datetime value.
function listRows(accounts: AccountSummary[]): string[][] {
  const shown = optionalColumns(accounts);
  return accounts.map((a) => [
    String(a.id),
    new Date(a.created_at).toISOString(),
    a.email,
    a.username,
    ...(shown.run ? [a.run_id === undefined ? '' : String(a.run_id)] : []),
    ...(shown.proxy ? [a.proxy ?? ''] : []),
    a.totp ? 'On' : 'Off',
    a.unconfirmed === true ? 'Unconfirmed' : '',
  ]);
}

function accountsTable(page: Page) {
  return page.getByRole('table', { name: 'Accounts, newest first' });
}

// Checks the columns, the rows and the total line of the list against the answer of the API.
// totalLine is the text that the screen shows for the total of the answer.
async function expectList(page: Page, answer: AccountPage, totalLine: string): Promise<void> {
  await expect.poll(() => shownRows(accountsTable(page))).toEqual(listRows(answer.accounts));
  await expect(accountsTable(page).getByRole('columnheader')).toHaveText(
    listColumns(answer.accounts),
  );
  await expect(page.getByText(totalLine, { exact: true })).toBeVisible();
}

function detailValue(page: Page, term: string) {
  return page.locator(`dt:text-is("${term}") + dd`);
}

test('the list shows the accounts of the server', async ({ page, request }) => {
  const answer = await readJSON<AccountPage>(request, '/api/accounts');
  expect(answer.accounts.map((a) => a.email)).toEqual([
    'totp@example.com',
    'unchecked@example.com',
    'plain@example.com',
  ]);

  await page.goto('/');
  await page
    .getByRole('navigation', { name: 'Main' })
    .getByRole('link', { name: 'Accounts' })
    .click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Accounts');
  await expectList(page, answer, `${String(answer.total)} accounts`);
  await expect(page.getByRole('link', { name: 'Next page' })).toHaveCount(0);

  await expectNoAxeViolations(page);
});

test('a time filter sends RFC 3339 and exports the same accounts', async ({ page, request }) => {
  await page.goto('/accounts');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Accounts');
  const form = page.getByRole('form', { name: 'Account filters' });
  await openSections(form);
  await form.getByLabel('Created from', { exact: true }).fill('2026-02-15T00:00');

  const answer = page.waitForResponse(
    (r) => new URL(r.url()).pathname === '/api/accounts' && new URL(r.url()).search !== '',
  );
  await form.getByRole('button', { name: 'Filter' }).click();
  const response = await answer;
  // The browser reads the form time as local time.
  const from = await page.evaluate(() => new Date(2026, 1, 15, 0, 0).toISOString());
  expect([...new URL(response.url()).searchParams]).toEqual([['created_from', from]]);
  expect(response.status()).toBe(200);
  const filtered = (await response.json()) as AccountPage;
  expect(filtered.accounts.map((a) => a.email)).toEqual(['totp@example.com']);
  await expectList(page, filtered, `${String(filtered.total)} matching account`);
  await expect(page).toHaveURL(/[?&]created_from=2026-02-15T00%3A00(&|$)/);
  // openSections above opened "More filters". The new page is on the same route, so the section
  // keeps that state.
  await expect(form.getByLabel('Created from', { exact: true })).toBeVisible();
  await expect(form.getByLabel('Created from', { exact: true })).toHaveValue('2026-02-15T00:00');

  const href = `/api/accounts/export?${new URLSearchParams({ format: 'userpass', created_from: from }).toString()}`;
  const link = page.getByRole('link', { name: 'email:password', exact: true });
  await expect(link).toHaveAttribute('href', href);
  const file = await request.get(href);
  expect(file.status()).toBe(200);
  expect(await file.text()).toBe('totp@example.com:Totp!pass3\n');

  await expectNoAxeViolations(page);
});

test('the detail reveals the TOTP secret and shows the live code', async ({
  page,
  context,
  request,
}) => {
  const list = await readJSON<AccountPage>(request, '/api/accounts?totp=true');
  expect(list.accounts).toHaveLength(1);
  const id = list.accounts[0]?.id ?? 0;
  const account = await readJSON<Account>(request, `/api/accounts/${String(id)}`);

  // The page reads the account again at the end of each code window.
  const codes: string[] = [];
  page.on('response', async (r) => {
    if (new URL(r.url()).pathname === `/api/accounts/${String(id)}`) {
      codes.push(((await r.json()) as Account).totp_code ?? '');
    }
  });

  await page.goto('/accounts');
  await page.getByRole('link', { name: account.email, exact: true }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(`Account ${String(id)}`);

  const secret = detailValue(page, 'TOTP secret');
  await expect(secret).toContainText('Hidden');
  await expect(page.getByText(account.totp_secret ?? '', { exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Reveal TOTP secret' }).click();
  await expect(secret.getByText(account.totp_secret ?? '', { exact: true })).toBeVisible();

  const code = detailValue(page, 'TOTP code');
  await expect(code).toHaveText(/^\s*\d{6} \(\d+ s left\)\s*$/);
  await expect
    .poll(async () => codes.includes((await code.textContent())?.trim().slice(0, 6) ?? ''))
    .toBe(true);

  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await page.getByRole('button', { name: 'Copy password' }).click();
  await expect(detailValue(page, 'Password').getByRole('status')).toHaveText('Copied');
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(account.password);

  await expect(page.getByRole('button', { name: 'Mark as checked' })).toHaveCount(0);
  await expectNoAxeViolations(page);
});

test('a confirm of the unconfirmed account removes its mark', async ({ page, request }) => {
  const list = await readJSON<AccountPage>(request, '/api/accounts?unconfirmed=true');
  expect(list.accounts.map((a) => a.email)).toEqual(['unchecked@example.com']);
  const id = String(list.accounts[0]?.id);

  await page.goto('/accounts');
  await page.getByRole('link', { name: 'Unconfirmed accounts' }).click();
  await expect(page).toHaveURL(/\/accounts\?unconfirmed=true$/);
  await expectList(page, list, `${String(list.total)} matching account`);
  await page.getByRole('link', { name: 'unchecked@example.com', exact: true }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(`Account ${id}`);

  // The header shows one status badge.
  const status = page.locator('main header').getByText(/^(Unconfirmed|Confirmed)$/);
  await expect(status).toHaveText('Unconfirmed');
  await expectNoAxeViolations(page);

  await page.getByRole('button', { name: 'Mark as checked' }).click();
  await expect(status).toHaveText('Confirmed');
  await expect(page.getByRole('button', { name: 'Mark as checked' })).toHaveCount(0);
  const account = await readJSON<Account>(request, `/api/accounts/${id}`);
  expect(account.unconfirmed).toBeUndefined();
  const left = await readJSON<AccountPage>(request, '/api/accounts?unconfirmed=true');
  expect(left.accounts).toEqual([]);
});
