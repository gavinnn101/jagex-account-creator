import { isHttpError } from '@sveltejs/kit';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { invalidate } from '$app/navigation';
import type { components } from '#lib/api.gen.d.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type Account = components['schemas']['Account'];

// The test page runs outside the SvelteKit router, so a real invalidate has no page to load.
vi.mock('$app/navigation', () => ({ invalidate: vi.fn(() => Promise.resolve()) }));

const full: Account = {
  id: 5,
  run_id: 2,
  email: 'b@example.com',
  username: 'bob',
  created_at: '2026-03-01T10:00:00Z',
  proxy: 'p.example.com:8080',
  browser_bytes_sent: 512,
  browser_bytes_received: 2048,
  http_bytes_sent: 1,
  http_bytes_received: 3072,
  unconfirmed: true,
  totp: true,
  password: 'S3cret!pass',
  totp_secret: 'JBSWY3DPEHPK3PXP',
  backup_codes: ['AAAA-1111', 'BBBB-2222'],
  totp_code: '123456',
  totp_remaining_seconds: 2,
};

const plain: Account = {
  id: 3,
  email: 'a@example.com',
  username: 'alice',
  created_at: '2026-01-01T10:00:00Z',
  totp: false,
  password: 'pw',
};

// The clock of the page reads this time, so the relative time is exact.
const now = Date.parse('2026-03-01T13:00:00Z');

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], now });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.mocked(invalidate).mockClear();
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

function renderPage(account: Account) {
  return render(Page, { data: { account }, params: { id: String(account.id) } });
}

type Screen = Awaited<ReturnType<typeof renderPage>>;

// The terms and values of the list of one section, with each run of white space as one space.
function rows(screen: Screen, section: string): [string, string][] {
  const terms = screen.getByRole('region', { name: section }).element().querySelectorAll('dt');
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

// Answers each request with the reply, and records the method and the path of each.
function stubFetch(reply: () => Promise<Response>) {
  const sent: string[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const request = new Request(input);
    sent.push(`${request.method} ${new URL(request.url).pathname}`);
    return reply();
  });
  return sent;
}

const hidden = expect.stringMatching(/^Hidden/) as string;

describe('load', () => {
  it('reads the account of the id in the path', async () => {
    const { requests, result } = loadWith(200, full, '5');
    await expect(result).resolves.toEqual({ account: full });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname])).toEqual([
      ['GET', '/api/accounts/5'],
    ]);
  });

  it('fails with the status of a refused read', async () => {
    const { result } = loadWith(404, { message: 'account 9 not found' }, '9');
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, 404)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Account read failed with HTTP 404' } });
  });
});

describe('page', () => {
  it('shows every field in the Login and Origin lists, with the secrets hidden', async () => {
    const screen = await renderPage(full);
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Account 5');
    expect(rows(screen, 'Login')).toEqual([
      ['Email', 'b@example.com'],
      ['Username', 'bob'],
      ['Password', hidden],
      ['TOTP code', '123456 (2 s left)'],
      ['TOTP secret', hidden],
      ['Backup codes', hidden],
    ]);
    expect(rows(screen, 'Origin')).toEqual([
      ['Created', 'Mar 1, 2026, 3:30:00 PM GMT+5:30 (3h ago)'],
      ['Run', 'Run 2'],
      ['Proxy', 'p.example.com:8080'],
      ['Browser', '512B sent, 2.0KiB received'],
      ['HTTP', '1B sent, 3.0KiB received'],
    ]);
    for (const secret of ['S3cret!pass', 'JBSWY3DPEHPK3PXP', 'AAAA-1111']) {
      expect(screen.container.textContent).not.toContain(secret);
    }
    const revealed: [string, string][] = [
      ['password', 'S3cret!pass'],
      ['TOTP secret', 'JBSWY3DPEHPK3PXP'],
      ['backup codes', 'AAAA-1111 BBBB-2222'],
    ];
    for (const [label, value] of revealed) {
      await screen.getByRole('button', { name: `Reveal ${label}` }).click();
      await expect.element(screen.getByText(value, { exact: true })).toBeVisible();
    }
    await expect
      .element(screen.getByRole('link', { name: 'All accounts' }))
      .toHaveAttribute('href', '/accounts');
  });

  it('links the run of the account', async () => {
    const screen = await renderPage(full);
    await expect
      .element(screen.getByRole('link', { name: 'Run 2' }))
      .toHaveAttribute('href', '/runs/2');
  });

  it('breaks the email at the @ before it breaks inside a word', async () => {
    const screen = await renderPage({ ...full, email: 'a.long.name@mail.example.com' });
    const email = screen.container.querySelector('dt + dd');
    // Svelte marks its blocks with comments, so the check drops them.
    expect(email?.innerHTML.replace(/<!--.*?-->/g, '').trim()).toBe(
      'a.long.name<wbr>@mail.example.com',
    );
    expect(screen.container.querySelectorAll('wbr')).toHaveLength(1);
  });

  it('shows the relative time of the clock of the page', async () => {
    // No live code: an hour of its 1 s countdown would hang a LiveCode mutant that never clears it.
    const screen = await renderPage({ ...plain, created_at: full.created_at });
    const created = () => rows(screen, 'Origin')[0]?.[1];
    expect(created()).toContain('(3h ago)');
    vi.advanceTimersByTime(30_000 * 120);
    await expect.poll(created).toContain('(4h ago)');
  });

  it('shows the status and 2FA badges and Mark as checked for an unconfirmed account', async () => {
    const screen = await renderPage(full);
    expect(badges(screen)).toEqual([
      ['Unconfirmed', 'warning'],
      ['2FA on', 'success'],
    ]);
    const header = screen.container.querySelector('header');
    const button = screen.getByRole('button', { name: 'Mark as checked' });
    await expect.element(button).toHaveAttribute('data-variant', 'primary');
    expect(header?.contains(button.element())).toBe(true);
    expect(header?.textContent).toContain('Check it by hand first.');
  });

  it('shows an account with no 2FA, no proxy and no run', async () => {
    const screen = await renderPage(plain);
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Account 3');
    expect(badges(screen)).toEqual([
      ['Confirmed', 'neutral'],
      ['2FA off', 'muted'],
    ]);
    expect(rows(screen, 'Login')).toEqual([
      ['Email', 'a@example.com'],
      ['Username', 'alice'],
      ['Password', hidden],
    ]);
    expect(rows(screen, 'Origin')).toEqual([
      ['Created', 'Jan 1, 2026, 3:30:00 PM GMT+5:30 (1mo ago)'],
      ['Run', 'None'],
      ['Proxy', 'None'],
      ['Browser', '0B sent, 0B received'],
      ['HTTP', '0B sent, 0B received'],
    ]);
    expect(screen.getByRole('link', { name: /^Run/ }).query()).toBeNull();
    expect(screen.getByRole('button', { name: 'Mark as checked' }).query()).toBeNull();
    expect(screen.container.textContent).not.toContain('Check it by hand first.');
  });

  it('shows no live code without its seconds', async () => {
    const screen = await renderPage({ ...plain, totp_code: '123456' });
    await expect.element(screen.getByRole('heading', { level: 1 })).toBeVisible();
    expect(screen.container.textContent).not.toContain('123456');
  });

  it('says why a TOTP secret has no live code', async () => {
    const screen = await renderPage({ ...plain, totp: true, totp_secret: 'not base32!' });
    expect(rows(screen, 'Login').slice(-2)).toEqual([
      ['TOTP code', 'none, the secret does not decode'],
      ['TOTP secret', hidden],
    ]);
  });

  it('shows no live code without the code', async () => {
    const screen = await renderPage({ ...plain, totp_remaining_seconds: 9 });
    await expect.element(screen.getByRole('heading', { level: 1 })).toBeVisible();
    expect(rows(screen, 'Login').map(([term]) => term)).not.toContain('TOTP code');
  });

  it('reads the account again at the end of the code window', async () => {
    const screen = await renderPage(full);
    await expect.element(screen.getByText('(2 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(1999);
    expect(invalidate).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/accounts/5');

    // A new read with the same seconds starts a new countdown.
    await screen.rerender({ data: { account: { ...full, totp_code: '654321' } } });
    await expect.element(screen.getByText('(2 s left)', { exact: true })).toBeVisible();
    vi.advanceTimersByTime(2000);
    expect(invalidate).toHaveBeenCalledTimes(2);
  });

  it('marks the account as checked, then reads it again', async () => {
    const sent = stubFetch(() => Promise.resolve(Response.json({ ...plain, id: 8, totp: false })));
    const screen = await renderPage({ ...plain, id: 8, unconfirmed: true });
    await screen.getByRole('button', { name: 'Mark as checked' }).click();
    await expect.poll(() => vi.mocked(invalidate).mock.calls).toEqual([['/api/accounts/8']]);
    expect(sent).toEqual(['POST /api/accounts/8/confirm']);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
    await expect
      .element(screen.getByRole('button', { name: 'Mark as checked' }))
      .not.toBeDisabled();
  });

  it('turns the button off while the confirm runs', async () => {
    let answer: (r: Response) => void = () => undefined;
    stubFetch(
      () =>
        new Promise<Response>((done) => {
          answer = done;
        }),
    );
    const screen = await renderPage({ ...plain, unconfirmed: true });
    const button = screen.getByRole('button', { name: 'Mark as checked' });
    await button.click();
    await expect.element(button).toBeDisabled();
    answer(Response.json({ message: 'account 3 not found' }, { status: 404 }));
    await expect.element(button).not.toBeDisabled();
  });

  it('shows the error of a refused confirm and reads nothing', async () => {
    stubFetch(() =>
      Promise.resolve(Response.json({ message: 'account 3 not found' }, { status: 404 })),
    );
    const screen = await renderPage({ ...plain, unconfirmed: true });
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
    await screen.getByRole('button', { name: 'Mark as checked' }).click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Confirm failed: account 3 not found');
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('shows a failed request, and clears it on the next confirm', async () => {
    let fail = true;
    stubFetch(() =>
      fail ? Promise.reject(new TypeError('network down')) : Promise.resolve(Response.json(plain)),
    );
    const screen = await renderPage({ ...plain, unconfirmed: true });
    const button = screen.getByRole('button', { name: 'Mark as checked' });
    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Confirm failed: TypeError: network down');
    fail = false;
    await button.click();
    await expect.poll(() => screen.container.querySelector('[role="alert"]')).toBeNull();
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/accounts/3');
  });
});
