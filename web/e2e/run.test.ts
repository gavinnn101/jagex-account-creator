import { expect, test, type Locator, type Page, type Response } from '@playwright/test';
import { readFile } from 'node:fs/promises';
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

type AccountPage = components['schemas']['AccountPage'];
type Config = components['schemas']['Config'];
type EmailConfig = components['schemas']['EmailConfig'];
type MailboxResult = components['schemas']['MailboxCheck'];
type Run = components['schemas']['Run'];
type RunPage = components['schemas']['RunPage'];
type RunState = components['schemas']['RunState'];
type ValidationResult = components['schemas']['ValidationResult'];

// The tests share the one run of serve, so they run in order.
test.describe.configure({ mode: 'serial' });

// A mailbox that accepts and never answers, so the run waits in its mailbox check until Stop.
let mailbox: AcceptingServer | undefined;
let mailboxPort = 0;

test.beforeAll(async () => {
  mailbox = await acceptingServer();
  mailboxPort = mailbox.port;
});

test.afterAll(async () => {
  await mailbox?.close();
});

async function openRun(page: Page) {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Run');
  // The run controls sit above the status, outside the config form.
  return {
    form: page.getByRole('form', { name: 'Run config' }),
    phase: page.getByRole('link', { name: /^Run phase: / }),
    runError: page.getByRole('alert', { name: 'Run error' }),
    start: page.getByRole('button', { name: 'Start', exact: true }),
    stop: page.getByRole('button', { name: 'Stop', exact: true }),
  };
}

// Clicks Start and returns the answer of the start request.
async function clickStart(page: Page, start: Locator): Promise<Response> {
  const answer = page.waitForResponse(
    (r) => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/run',
  );
  await start.click();
  return answer;
}

// Checks that the answer is a 422 and that the run controls list its errors. It returns the errors.
async function expectErrorsShown(page: Page, answer: Response): Promise<string[]> {
  expect(answer.status()).toBe(422);
  const { errors } = (await answer.json()) as ValidationResult;
  await expect(
    page.getByRole('alert', { name: 'Request errors' }).getByRole('listitem'),
  ).toHaveText(errors);
  return errors;
}

// Reads one value of each labelled control of the form, keyed by its label.
async function shown<T>(
  form: Locator,
  labels: string[],
  read: (control: Locator) => Promise<T>,
): Promise<Record<string, T>> {
  const entries = await Promise.all(
    labels.map(async (label): Promise<[string, T]> => [
      label,
      await read(form.getByLabel(label, { exact: true })),
    ]),
  );
  return Object.fromEntries(entries);
}

// The run of the tests stops in its mailbox check, so it has no attempt.
function expectNoAttempt(run: Run): void {
  expect(run.counts).toEqual({
    attempts: 0,
    accounts: 0,
    unconfirmed: 0,
    failures: {},
    browser_bytes_sent: 0,
    browser_bytes_received: 0,
    http_bytes_sent: 0,
    http_bytes_received: 0,
  });
}

// The duration of an ended run under one hour, as m:ss.
function shownDuration(run: Run): string {
  expect(run.ended_at).toBeDefined();
  const span = Math.floor((Date.parse(run.ended_at ?? '') - Date.parse(run.started_at)) / 1000);
  // The host clock can step back, so the end can come before the start. The screen shows 0:00 then.
  const seconds = Math.max(0, span);
  expect(seconds).toBeLessThan(3600);
  return `${String(Math.floor(seconds / 60))}:${String(seconds % 60).padStart(2, '0')}`;
}

// The cells of a list row as the screen shows a run that ended with an error and has no attempt.
// The page hides the Unconfirmed and the Failures column, because no run has a value for them.
function runCells(run: Run): string[] {
  expectNoAttempt(run);
  expect(run.error).toBeDefined();
  return [
    String(run.id),
    new Date(run.started_at).toISOString(),
    shownDuration(run),
    'Error',
    '0',
    '0',
    '—',
    run.source,
  ];
}

// The terms and values of each list of the detail as the screen shows an ended run with no attempt.
function detailFields(run: Run): Record<string, string[][]> {
  expectNoAttempt(run);
  expect(run.ended_at).toBeDefined();
  return {
    Timing: [
      ['Source', run.source],
      ['Started', new Date(run.started_at).toISOString()],
      ['Ended', new Date(run.ended_at ?? '').toISOString()],
      ['Duration', shownDuration(run)],
    ],
    Results: [
      ['Attempts', '0'],
      ['Accounts', '0'],
      ['Unconfirmed', '0'],
      ['Success rate', '—'],
      ['Failures', 'None'],
    ],
  };
}

// The term and the value of each pair of the list in scope. A value with a time element gives its
// datetime, because the shown relative time changes as the clock moves.
function shownTerms(scope: Locator): Promise<string[][]> {
  return scope.locator('dt').evaluateAll((terms) =>
    terms.map((dt) => {
      const dd = dt.nextElementSibling;
      const time = dd?.querySelector('time[datetime]')?.getAttribute('datetime');
      return [dt.textContent.trim(), time ?? dd?.textContent.replace(/\s+/g, ' ').trim() ?? ''];
    }),
  );
}

// Each value of the config under its dotted key, the key of the config file. A list is one value.
function dotted(values: object, prefix = ''): string[][] {
  return Object.entries(values).flatMap(([key, value]: [string, unknown]) => {
    if (Array.isArray(value)) return [[prefix + key, value.join(', ')]];
    return value instanceof Object
      ? dotted(value, `${prefix}${key}.`)
      : [[prefix + key, String(value)]];
  });
}

test('the form shows the saved config', async ({ page, request }) => {
  const config = await readJSON<Config>(request, '/api/config');
  const { form } = await openRun(page);

  const text: Record<string, string> = {
    Database: config.database,
    'Chrome timeout': config.chrome.timeout,
    'IMAP host': config.email.imap_host,
    'IMAP port': String(config.email.imap_port),
    'IMAP user': config.email.imap_user,
    'IMAP password': '',
    'IMAP domains': config.email.imap_domains.join('\n'),
    'Password length': String(config.account.password_length),
    'Account count': String(config.concurrency.account_count),
    'Max workers': String(config.concurrency.max_workers),
    'Max browsers': String(config.concurrency.max_browsers),
    'Account timeout': config.concurrency.account_timeout,
  };
  expect(await shown(form, Object.keys(text), (control) => control.inputValue())).toEqual(text);

  const checked: Record<string, boolean> = {
    Debug: config.debug,
    'Direct, with no proxy': config.proxy.direct,
    Headless: config.chrome.headless,
    'Enable TOTP': config.account.enable_totp,
  };
  expect(await shown(form, Object.keys(checked), (control) => control.isChecked())).toEqual(
    checked,
  );

  const placeholder = config.email.imap_pass_set === true ? 'Stored. Empty keeps it.' : null;
  expect(await form.getByLabel('IMAP password', { exact: true }).getAttribute('placeholder')).toBe(
    placeholder,
  );

  await expectNoAxeViolations(page);
});

test('a start with the defaults shows the errors of the server', async ({ page }) => {
  const { start } = await openRun(page);

  const errors = await expectErrorsShown(page, await clickStart(page, start));
  expect(errors).toContain('email.imap_host is required');

  await expectNoAxeViolations(page);
});

test('an empty number field sends 0', async ({ page }) => {
  const { form, start } = await openRun(page);
  await openSections(form);
  await form.getByLabel('IMAP port', { exact: true }).fill('');

  const answer = await clickStart(page, start);
  const sent = answer.request().postDataJSON() as { email: { imap_port: unknown } };
  expect(sent.email.imap_port).toBe(0);
  const errors = await expectErrorsShown(page, answer);
  expect(errors).toContain('email.imap_port must be between 1 and 65535, got 0');

  await expectNoAxeViolations(page);
});

test('a start with every mailbox field set runs', async ({ page }) => {
  const { form, phase, start } = await openRun(page);
  await openSections(form);
  await form.getByLabel('IMAP host', { exact: true }).fill('127.0.0.1');
  await form.getByLabel('IMAP port', { exact: true }).fill(String(mailboxPort));
  await form.getByLabel('IMAP user', { exact: true }).fill('e2e');
  await form.getByLabel('IMAP password', { exact: true }).fill('e2e-password');
  await form.getByLabel('IMAP domains', { exact: true }).fill('example.com');

  const answer = await clickStart(page, start);
  expect(answer.status()).toBe(202);
  await expect(phase).toHaveAccessibleName('Run phase: Running');
  await expect(
    page.getByRole('img', { name: /^Saved accounts: 0\. Failed attempts: 0\.$/ }),
  ).toBeVisible();

  await expectNoAxeViolations(page);
});

test('Stop ends the run with the mailbox error', async ({ page, request }) => {
  const { phase, runError, stop } = await openRun(page);
  await expect(phase).toHaveAccessibleName('Run phase: Running');

  await stop.click();
  await expect(phase).toHaveAccessibleName('Run phase: Ended');
  const state = await readJSON<RunState>(request, '/api/run');
  expect(state.phase).toBe('ended');
  expect(state.error).toMatch(/^checking the mailbox: /);
  await expect(runError).toHaveText(`Run error: ${state.error ?? ''}`);

  await expectNoAxeViolations(page);
});

test('the Logs screen shows the lines of the run and downloads its log file', async ({
  page,
  request,
}) => {
  const state = await readJSON<RunState>(request, '/api/run');
  const runId = String(state.run_id);
  await page.goto('/');
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Logs' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Logs');
  const list = page.getByRole('region', { name: 'Log lines' });
  await expect(list.getByText('run started', { exact: true })).toBeVisible();
  await expect(list.getByText('run ended', { exact: true })).toBeVisible();

  const file = await request.get(`/api/runs/${runId}/log`);
  expect(file.status()).toBe(200);
  const download = page.waitForEvent('download');
  await page.getByRole('link', { name: 'Download the log file' }).click();
  const saved = await download;
  expect(saved.suggestedFilename()).toBe(`run-${runId}.log`);
  expect(await readFile(await saved.path())).toEqual(await file.body());

  await expectNoAxeViolations(page);
});

test('the Runs screen lists the runs of the server', async ({ page, request }) => {
  const state = await readJSON<RunState>(request, '/api/run');
  const list = await readJSON<RunPage>(request, '/api/runs');
  expect(list.runs.map((r) => r.id)).toEqual([state.run_id]);
  expect(list.next_before).toBeUndefined();

  await page.goto('/');
  await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Runs' }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Runs');
  const table = page.getByRole('table', { name: 'Runs, newest first' });
  await expect.poll(() => shownRows(table)).toEqual(list.runs.map((r) => runCells(r)));
  await expect(page.getByRole('link', { name: 'Next page' })).toHaveCount(0);

  await expectNoAxeViolations(page);
});

test('the run detail shows the run and links its accounts', async ({ page, request }) => {
  const state = await readJSON<RunState>(request, '/api/run');
  const id = String(state.run_id);
  const run = await readJSON<Run>(request, `/api/runs/${id}`);
  expect(run.error).toMatch(/^checking the mailbox: /);
  const config = dotted(run.config);
  expect(config).toContainEqual(['email.imap_pass_set', 'true']);

  await page.goto('/runs');
  await page.getByRole('link', { name: id, exact: true }).click();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(`Run ${id}`);
  await expect(page.locator('main header [data-tone]')).toHaveText('Error');
  await expect(page.getByText(/^Run error: /)).toHaveText(`Run error: ${run.error ?? ''}`);
  for (const [name, fields] of Object.entries(detailFields(run))) {
    const list = page.getByRole('region', { name, exact: true });
    await expect.poll(() => shownTerms(list)).toEqual(fields);
  }
  // The run has no attempt, so it has no byte to show.
  await expect(page.getByRole('region', { name: 'Traffic', exact: true })).toHaveText(
    'Traffic None',
  );
  // The table sits in the closed All values section, so its locator must include hidden elements.
  const configTable = page.getByRole('table', { name: 'Config of the run', includeHidden: true });
  await expect.poll(() => shownRows(configTable)).toEqual(config);
  await expect(page.getByRole('link', { name: 'Download the log file' })).toHaveAttribute(
    'href',
    `/api/runs/${id}/log`,
  );
  await expectNoAxeViolations(page);

  const accounts = await readJSON<AccountPage>(request, `/api/accounts?run=${id}`);
  expect(accounts.accounts).toEqual([]);
  expect(accounts.total).toBe(0);
  await page.getByRole('link', { name: 'Accounts of this run' }).click();
  await expect(page).toHaveURL(new RegExp(`/accounts\\?run=${id}$`));
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Accounts');
  await expect(page.getByText('No account matches these filters.')).toBeVisible();
  await expect(page.getByRole('table', { name: 'Accounts, newest first' })).toHaveCount(0);

  await expectNoAxeViolations(page);
});

// Values that differ from the defaults and from the run above. The save makes them the config of serve.
const savedEmail: Record<string, string> = {
  'IMAP host': 'imap.example.com',
  'IMAP port': '993',
  'IMAP user': 'e2e-saved',
  'IMAP domains': 'saved.example.com\nsaved.example.org',
};

test('Save stores the config, and a reload shows it', async ({ page, request }) => {
  const { form } = await openRun(page);
  await openSections(form);
  for (const [label, value] of Object.entries(savedEmail)) {
    await form.getByLabel(label, { exact: true }).fill(value);
  }
  const pass = form.getByLabel('IMAP password', { exact: true });
  // Validation refuses a save with no IMAP password.
  await pass.fill('e2e-saved-password');

  const answer = page.waitForResponse(
    (r) => r.request().method() === 'PUT' && new URL(r.url()).pathname === '/api/config',
  );
  await form.getByRole('button', { name: 'Save', exact: true }).click();
  const response = await answer;
  expect(response.status()).toBe(200);
  await expect(form.getByRole('status')).toHaveText('Config saved.');
  await expect(pass).toHaveValue('');
  await expect(pass).toHaveAttribute('placeholder', 'Stored. Empty keeps it.');

  const config = await readJSON<Config>(request, '/api/config');
  expect(config).toEqual(await response.json());
  expect(config.email).toEqual({
    imap_host: 'imap.example.com',
    imap_port: 993,
    imap_user: 'e2e-saved',
    imap_pass_set: true,
    imap_domains: ['saved.example.com', 'saved.example.org'],
  });

  await page.reload();
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Run');
  expect(await shown(form, Object.keys(savedEmail), (control) => control.inputValue())).toEqual(
    savedEmail,
  );
  await expect(pass).toHaveValue('');
  await expect(pass).toHaveAttribute('placeholder', 'Stored. Empty keeps it.');

  await expectNoAxeViolations(page);
});

test('a mailbox check of a closed port shows the error of the server', async ({
  page,
  request,
}) => {
  const port = await closedPort();
  const { form } = await openRun(page);
  await openSections(form);
  await form.getByLabel('IMAP host', { exact: true }).fill('127.0.0.1');
  await form.getByLabel('IMAP port', { exact: true }).fill(String(port));

  const answer = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/checks/mailbox');
  await form.getByRole('button', { name: 'Check mailbox', exact: true }).click();
  const response = await answer;
  expect(response.status()).toBe(200);
  const sent = response.request().postDataJSON() as EmailConfig;
  expect([sent.imap_host, sent.imap_port]).toEqual(['127.0.0.1', port]);

  const direct = await request.post('/api/checks/mailbox', { data: sent });
  expect(direct.status()).toBe(200);
  const result = (await direct.json()) as MailboxResult;
  expect(result.ok).toBe(false);
  expect(await response.json()).toEqual(result);
  await expect(form.getByRole('alert').filter({ hasText: /^Mailbox check failed: / })).toHaveText(
    `Mailbox check failed: ${result.error ?? ''}`,
  );

  await expectNoAxeViolations(page);
});
