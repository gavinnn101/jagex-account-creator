import { isHttpError } from '@sveltejs/kit';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../../app.css';
import type { components } from '#lib/api.gen.d.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type Run = components['schemas']['Run'];

const ended: Run = {
  id: 5,
  source: 'cli',
  started_at: '2026-10-21T09:00:00Z',
  ended_at: '2026-10-21T10:02:03Z',
  error: 'too many failures',
  config: {
    account: { enable_totp: true, password_length: 16 },
    chrome: { headless: true, timeout: '30s' },
    concurrency: { account_count: 10, account_timeout: '1m0s', max_browsers: 0, max_workers: 2 },
    database: 'creator.db',
    debug: false,
    email: {
      auth: 'password',
      imap_domains: ['example.com'],
      imap_host: 'imap.example.com',
      imap_pass_set: true,
      imap_port: 993,
      imap_user: 'user',
      oauth2: { client_id: '', client_secret_set: false, refresh_token_set: false, token_url: '' },
    },
    proxy: { direct: false },
  },
  counts: {
    attempts: 4,
    accounts: 2,
    unconfirmed: 1,
    failures: { proxy: 1, email: 1 },
    browser_bytes_sent: 512,
    browser_bytes_received: 2048,
    http_bytes_sent: 1,
    http_bytes_received: 3072,
  },
};

const noBytes = {
  browser_bytes_sent: 0,
  browser_bytes_received: 0,
  http_bytes_sent: 0,
  http_bytes_received: 0,
};

const running: Run = {
  id: 8,
  source: 'serve',
  started_at: '2026-10-22T09:00:00Z',
  config: ended.config,
  counts: { attempts: 0, accounts: 0, unconfirmed: 0, failures: {}, ...noBytes },
};

const clean: Run = {
  id: 6,
  source: 'cli',
  started_at: ended.started_at,
  ended_at: '2026-10-21T10:02:03Z',
  config: ended.config,
  counts: ended.counts,
};

// The clock of the page reads this time, so each relative time and the live duration are exact.
const now = Date.parse('2026-10-22T10:00:00Z');

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], now });
});

afterEach(() => {
  vi.useRealTimers();
});

function loadWith(status: number, body: unknown, id: string) {
  const requests: Request[] = [];
  const fetch = (input: RequestInfo | URL): Promise<Response> => {
    requests.push(new Request(input));
    return Promise.resolve(Response.json(body, { status }));
  };
  const event = { fetch, params: { id } } as unknown as Parameters<PageLoad>[0];
  return { requests, result: load(event) };
}

function renderPage(run: Run) {
  return render(Page, { data: { run }, params: { id: String(run.id) } });
}

type Screen = Awaited<ReturnType<typeof renderPage>>;

function section(screen: Screen, name: string): HTMLElement {
  return screen.getByRole('region', { name, exact: true }).element() as HTMLElement;
}

// The terms and values of the list of one section, with each run of white space as one space.
function rows(screen: Screen, name: string): [string, string][] {
  const terms = section(screen, name).querySelectorAll('dt');
  return [...terms].map((dt) => [
    dt.textContent,
    dt.nextElementSibling?.textContent.replace(/\s+/g, ' ').trim() ?? '',
  ]);
}

// The text and the tone of each badge of the header.
function badges(screen: Screen): [string, string | undefined][] {
  const header = screen.container.querySelector('header');
  return [...(header?.querySelectorAll<HTMLElement>('[data-tone]') ?? [])].map((b) => [
    b.textContent.trim(),
    b.dataset.tone,
  ]);
}

function runError(screen: Screen): HTMLElement | undefined {
  return [...screen.container.querySelectorAll<HTMLElement>('p')].find((p) =>
    p.textContent.startsWith('Run error: '),
  );
}

// The computed color of a color token of app.css.
function tokenColor(token: string): string {
  const probe = document.createElement('span');
  probe.style.color = `var(--color-${token})`;
  document.body.append(probe);
  const color = getComputedStyle(probe).color;
  probe.remove();
  return color;
}

describe('load', () => {
  it('reads the run of the id in the path', async () => {
    const { requests, result } = loadWith(200, ended, '5');
    await expect(result).resolves.toEqual({ run: ended });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname])).toEqual([
      ['GET', '/api/runs/5'],
    ]);
  });

  it('fails with the status of a refused read', async () => {
    const { result } = loadWith(404, { message: 'run 9 not found' }, '9');
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, 404)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Run read failed with HTTP 404' } });
  });
});

describe('page', () => {
  it('shows a run that ended with an error, with its error in the danger color', async () => {
    const screen = await renderPage(ended);
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Run 5');
    expect(badges(screen)).toEqual([['Error', 'danger']]);
    const error = runError(screen);
    expect(error?.textContent).toBe('Run error: too many failures');
    expect(error === undefined ? '' : getComputedStyle(error).color).toBe(tokenColor('danger'));
  });

  it('shows the timing, the results and the traffic of the run', async () => {
    const screen = await renderPage(ended);
    expect(rows(screen, 'Timing')).toEqual([
      ['Source', 'cli'],
      ['Started', 'Oct 21, 2026, 2:30:00 PM GMT+5:30 (1d ago)'],
      ['Ended', 'Oct 21, 2026, 3:32:03 PM GMT+5:30 (23h ago)'],
      ['Duration', '1:02:03'],
    ]);
    expect(rows(screen, 'Results')).toEqual([
      ['Attempts', '4'],
      ['Accounts', '2'],
      ['Unconfirmed', '1'],
      ['Success rate', '50.0%'],
      ['Failures', 'email 1, proxy 1'],
    ]);
    expect(rows(screen, 'Traffic')).toEqual([
      ['Browser', '512B sent, 2.0KiB received'],
      ['HTTP', '1B sent, 3.0KiB received'],
    ]);
    const times = [...section(screen, 'Timing').querySelectorAll('time')].map((t) =>
      t.getAttribute('datetime'),
    );
    expect(times).toEqual(['2026-10-21T09:00:00.000Z', '2026-10-21T10:02:03.000Z']);
  });

  it('shows the digest of each config section, and every value in a closed section', async () => {
    const screen = await renderPage(ended);
    expect(rows(screen, 'Config')).toEqual([
      [
        'Run settings',
        '10 accounts, 2 workers, one browser per worker, 1m0s timeout, through proxies',
      ],
      ['Mailbox', 'user at imap.example.com:993'],
      ['Accounts', '16-character passwords, TOTP on'],
      ['Chrome', 'Headless, 30s timeout'],
      ['General', 'Debug off, creator.db'],
    ]);
    const details = section(screen, 'Config').querySelector('details');
    expect(details?.open).toBe(false);
    expect(details?.querySelector('summary')?.textContent.trim()).toBe('All values');
    const table = screen.getByRole('table', { name: 'Config of the run', includeHidden: true });
    expect(details?.contains(table.element())).toBe(true);
    const cells = [...table.element().querySelectorAll('tr')].map((row) =>
      [...row.children].map((cell) => cell.textContent.trim()),
    );
    expect(cells).toEqual([
      ['Key', 'Value'],
      ['account.enable_totp', 'true'],
      ['account.password_length', '16'],
      ['chrome.headless', 'true'],
      ['chrome.timeout', '30s'],
      ['concurrency.account_count', '10'],
      ['concurrency.account_timeout', '1m0s'],
      ['concurrency.max_browsers', '0'],
      ['concurrency.max_workers', '2'],
      ['database', 'creator.db'],
      ['debug', 'false'],
      ['email.auth', 'password'],
      ['email.imap_domains', 'example.com'],
      ['email.imap_host', 'imap.example.com'],
      ['email.imap_pass_set', 'true'],
      ['email.imap_port', '993'],
      ['email.imap_user', 'user'],
      ['email.oauth2.client_id', ''],
      ['email.oauth2.client_secret_set', 'false'],
      ['email.oauth2.refresh_token_set', 'false'],
      ['email.oauth2.token_url', ''],
      ['proxy.direct', 'false'],
    ]);
    expect(screen.getByRole('table', { name: 'Failures by category' }).query()).toBeNull();
  });

  it('links all runs, the accounts of the run and its log file', async () => {
    const screen = await renderPage(ended);
    const links = screen.getByRole('link').elements();
    expect(links.map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['All runs', '/runs'],
      ['Accounts of this run', '/accounts?run=5'],
      ['Download the log file', '/api/runs/5/log'],
    ]);
    const header = screen.container.querySelector('header');
    expect(header?.contains(links[1] ?? null)).toBe(true);
    expect(header?.contains(links[2] ?? null)).toBe(true);
    const log = screen.getByRole('link', { name: 'Download the log file' }).element();
    expect(log.getAttribute('rel')).toBe('external');
    expect(log.hasAttribute('download')).toBe(true);
  });

  it('shows a clean end as Ended, with no run error', async () => {
    const screen = await renderPage(clean);
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Run 6');
    expect(badges(screen)).toEqual([['Ended', 'neutral']]);
    expect(runError(screen)).toBeUndefined();
  });

  it('shows a run in progress with its elapsed time, no end and no traffic', async () => {
    const screen = await renderPage(running);
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Run 8');
    expect(badges(screen)).toEqual([['In progress', 'accent']]);
    expect(runError(screen)).toBeUndefined();
    expect(rows(screen, 'Timing')).toEqual([
      ['Source', 'serve'],
      ['Started', 'Oct 22, 2026, 2:30:00 PM GMT+5:30 (1h ago)'],
      ['Duration', '1:00:00'],
    ]);
    expect(rows(screen, 'Results')).toEqual([
      ['Attempts', '0'],
      ['Accounts', '0'],
      ['Unconfirmed', '0'],
      ['Success rate', '—'],
      ['Failures', 'None'],
    ]);
    expect(section(screen, 'Traffic').querySelector('dl')).toBeNull();
    expect(section(screen, 'Traffic').textContent.replace(/\s+/g, ' ').trim()).toBe('Traffic None');
  });

  it('moves the start and the elapsed time on with the clock of the page', async () => {
    const screen = await renderPage(running);
    const timing = () => rows(screen, 'Timing').slice(1);
    vi.advanceTimersByTime(30_000);
    await expect.poll(timing).toEqual([
      ['Started', 'Oct 22, 2026, 2:30:00 PM GMT+5:30 (1h ago)'],
      ['Duration', '1:00:30'],
    ]);
    vi.advanceTimersByTime(30_000 * 119);
    await expect.poll(timing).toEqual([
      ['Started', 'Oct 22, 2026, 2:30:00 PM GMT+5:30 (2h ago)'],
      ['Duration', '2:00:00'],
    ]);
  });

  it.each([
    ['Browser', { browser_bytes_received: 10 }, '0B sent, 10B received'],
    ['HTTP', { http_bytes_sent: 10 }, '10B sent, 0B received'],
  ])('shows only the %s row of a phase with bytes', async (phase, bytes, text) => {
    const screen = await renderPage({ ...running, counts: { ...running.counts, ...bytes } });
    expect(rows(screen, 'Traffic')).toEqual([[phase, text]]);
  });
});
