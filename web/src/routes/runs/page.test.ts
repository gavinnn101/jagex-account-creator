import { isHttpError } from '@sveltejs/kit';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import type { components } from '#lib/api.gen.d.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type Run = components['schemas']['Run'];
type RunPage = components['schemas']['RunPage'];

const config: Run['config'] = {
  debug: false,
  database: 'creator.db',
  chrome: { headless: true, timeout: '30s' },
  proxy: { direct: false },
  email: { imap_host: 'h', imap_port: 993, imap_user: 'u', imap_domain: 'd' },
  account: { password_length: 16, enable_totp: true },
  concurrency: { max_workers: 2, max_browsers: 0, account_count: 10, account_timeout: '1m0s' },
};

const counts: Run['counts'] = {
  attempts: 0,
  accounts: 0,
  unconfirmed: 0,
  failures: {},
  browser_bytes_sent: 0,
  browser_bytes_received: 0,
  http_bytes_sent: 0,
  http_bytes_received: 0,
};

const live: Run = {
  id: 7,
  source: 'serve',
  started_at: '2026-10-22T10:00:00Z',
  config,
  counts: { ...counts, attempts: 4, accounts: 3, unconfirmed: 1, failures: { proxy: 1 } },
};

const failed: Run = {
  id: 6,
  source: 'serve',
  started_at: '2026-10-22T09:00:00Z',
  ended_at: '2026-10-22T09:00:04Z',
  error: 'checking the mailbox: refused',
  config,
  counts: { ...counts, attempts: 1, accounts: 1 },
};

const clean: Run = {
  id: 5,
  source: 'cli',
  started_at: '2026-10-21T09:00:00Z',
  ended_at: '2026-10-21T09:02:30Z',
  config,
  counts,
};

const runs: RunPage = { runs: [live, failed, clean], next_before: 5 };

// The clock of the page reads this time, so each relative time and the live duration are exact.
const now = Date.parse('2026-10-22T10:05:00Z');

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], now });
});

afterEach(() => {
  vi.useRealTimers();
});

function loadWith(status: number, body: unknown, search: string) {
  const requests: Request[] = [];
  const fetch = (input: RequestInfo | URL): Promise<Response> => {
    requests.push(new Request(input));
    return Promise.resolve(Response.json(body, { status }));
  };
  const url = new URL(`http://127.0.0.1/runs${search}`);
  const event = { fetch, url } as unknown as Parameters<PageLoad>[0];
  return { requests, result: load(event) };
}

function renderPage(page: RunPage = runs) {
  return render(Page, { data: { page }, params: {} });
}

type Screen = Awaited<ReturnType<typeof renderPage>>;

function hrefs(links: Element[]): [string | null, string | null][] {
  return links.map((a) => [a.textContent, a.getAttribute('href')]);
}

function table(screen: Screen) {
  return screen.getByRole('table', { name: 'Runs, newest first' });
}

function cells(screen: Screen): string[][] {
  return table(screen)
    .getByRole('row')
    .elements()
    .map((row) => [...row.children].map((cell) => cell.textContent.trim()));
}

// The text of the page, with each run of white space as one space.
function text(screen: Screen): string {
  return screen.container.textContent.replace(/\s+/g, ' ').trim();
}

describe('load', () => {
  it('reads the page of the before in the page query', async () => {
    const { requests, result } = loadWith(200, runs, '?before=9');
    await expect(result).resolves.toEqual({ page: runs });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname, new URL(r.url).search])).toEqual(
      [['GET', '/api/runs', '?before=9']],
    );
  });

  it('sends a bad before as it is', async () => {
    const { requests, result } = loadWith(200, runs, '?before=x%201');
    await result;
    expect(new URL(requests[0]?.url ?? '').searchParams.get('before')).toBe('x 1');
  });

  it.each(['', '?before='])('reads the first page with the query %j', async (search) => {
    const { requests, result } = loadWith(200, runs, search);
    await expect(result).resolves.toEqual({ page: runs });
    expect(new URL(requests[0]?.url ?? '').search).toBe('');
  });

  it('fails with the status of a refused read', async () => {
    const { result } = loadWith(400, { message: 'bad before' }, '?before=0');
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, 400)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Run read failed with HTTP 400' } });
  });
});

describe('page', () => {
  it('lists the summary of each run', async () => {
    const screen = await renderPage();
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Runs');
    expect(cells(screen)).toEqual([
      [
        'ID',
        'Started',
        'Duration',
        'Status',
        'Attempts',
        'Accounts',
        'Unconfirmed',
        'Success rate',
        'Failures',
        'Source',
      ],
      ['7', '5m ago', '5:00', 'In progress', '4', '3', '1', '75.0%', 'proxy 1', 'serve'],
      ['6', '1h ago', '0:04', 'Error', '1', '1', '0', '100.0%', '', 'serve'],
      ['5', '1d ago', '2:30', '', '0', '0', '0', '—', '', 'cli'],
    ]);
    expect(text(screen)).not.toContain('No runs yet.');
  });

  it('links the detail and the accounts of each run', async () => {
    const screen = await renderPage();
    expect(hrefs(table(screen).getByRole('link').elements())).toEqual([
      ['7', '/runs/7'],
      ['3', '/accounts?run=7'],
      ['6', '/runs/6'],
      ['1', '/accounts?run=6'],
      ['5', '/runs/5'],
      ['0', '/accounts?run=5'],
    ]);
    const accounts = screen.getByRole('link', { name: 'Accounts of run 7: 3' });
    await expect.element(accounts).toHaveAttribute('title', 'Accounts of run 7');
    await expect
      .element(screen.getByRole('link', { name: 'Next page' }))
      .toHaveAttribute('href', '/runs?before=5');
  });

  it('shows the start as a time element with the full time', async () => {
    const screen = await renderPage();
    const times = [...table(screen).element().querySelectorAll('time')].map((t) => [
      t.getAttribute('datetime'),
      t.getAttribute('title'),
    ]);
    expect(times).toEqual([
      ['2026-10-22T10:00:00.000Z', 'Oct 22, 2026, 3:30:00 PM GMT+5:30'],
      ['2026-10-22T09:00:00.000Z', 'Oct 22, 2026, 2:30:00 PM GMT+5:30'],
      ['2026-10-21T09:00:00.000Z', 'Oct 21, 2026, 2:30:00 PM GMT+5:30'],
    ]);
  });

  it('shows the status as a badge, with the run error as its title', async () => {
    const screen = await renderPage();
    const badges = table(screen)
      .getByRole('row')
      .elements()
      .slice(1)
      .map((row) =>
        [...row.querySelectorAll<HTMLElement>('[data-tone]')].map((b) => [
          b.textContent.trim(),
          b.dataset.tone,
          b.parentElement?.getAttribute('title'),
        ]),
      );
    expect(badges).toEqual([
      [['In progress', 'accent', null]],
      [['Error', 'danger', 'checking the mailbox: refused']],
      [],
    ]);
  });

  it('moves the start and the live duration on with the clock of the page', async () => {
    const screen = await renderPage();
    const shown = () => cells(screen)[1]?.slice(1, 3);
    expect(shown()).toEqual(['5m ago', '5:00']);
    vi.advanceTimersByTime(30_000);
    await expect.poll(shown).toEqual(['5m ago', '5:30']);
    vi.advanceTimersByTime(30_000);
    await expect.poll(shown).toEqual(['6m ago', '6:00']);
    expect(cells(screen)[3]?.[2]).toBe('2:30');
  });

  it('keeps each cell on one line', async () => {
    const screen = await renderPage();
    for (const cell of table(screen).getByRole('cell').elements()) {
      expect(getComputedStyle(cell).whiteSpace).toBe('nowrap');
    }
  });

  it('hides Status, Unconfirmed and Failures when no run of the page has a value', async () => {
    const screen = await renderPage({ runs: [clean] });
    expect(cells(screen)).toEqual([
      ['ID', 'Started', 'Duration', 'Attempts', 'Accounts', 'Success rate', 'Source'],
      ['5', '1d ago', '2:30', '0', '0', '—', 'cli'],
    ]);
  });

  it('shows each optional column on its own', async () => {
    const status = await renderPage({ runs: [failed] });
    expect(cells(status)).toEqual([
      ['ID', 'Started', 'Duration', 'Status', 'Attempts', 'Accounts', 'Success rate', 'Source'],
      ['6', '1h ago', '0:04', 'Error', '1', '1', '100.0%', 'serve'],
    ]);
    await status.unmount();

    const unconfirmed = await renderPage({
      runs: [{ ...clean, counts: { ...counts, accounts: 2, unconfirmed: 2 } }],
    });
    expect(cells(unconfirmed)).toEqual([
      [
        'ID',
        'Started',
        'Duration',
        'Attempts',
        'Accounts',
        'Unconfirmed',
        'Success rate',
        'Source',
      ],
      ['5', '1d ago', '2:30', '0', '2', '2', '—', 'cli'],
    ]);
    await unconfirmed.unmount();

    const failures = await renderPage({
      runs: [{ ...clean, counts: { ...counts, attempts: 2, failures: { email: 2 } } }],
    });
    expect(cells(failures)).toEqual([
      ['ID', 'Started', 'Duration', 'Attempts', 'Accounts', 'Success rate', 'Failures', 'Source'],
      ['5', '1d ago', '2:30', '2', '0', '0.0%', 'email 2', 'cli'],
    ]);
  });

  it('asks for a run when the store has no run', async () => {
    const screen = await renderPage({ runs: [] });
    await expect
      .element(screen.getByRole('link', { name: 'Start a run' }))
      .toHaveAttribute('href', '/');
    expect(text(screen)).toContain('No runs yet. Start a run');
    expect(screen.getByRole('table').query()).toBeNull();
    expect(screen.getByRole('link', { name: 'Next page' }).query()).toBeNull();
  });

  it('has no next page link on the last page', async () => {
    const screen = await renderPage({ runs: [clean] });
    await expect.element(table(screen)).toBeVisible();
    expect(screen.getByRole('link', { name: 'Next page' }).query()).toBeNull();
  });
});
