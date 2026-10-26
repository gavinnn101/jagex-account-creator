import { isHttpError } from '@sveltejs/kit';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { userEvent } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import type { Filters } from '#lib/accounts/query.ts';
import type { components } from '#lib/api.gen.d.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type AccountPage = components['schemas']['AccountPage'];
type AccountSummary = components['schemas']['AccountSummary'];

const none: Filters = {
  run: '',
  unconfirmed: '',
  totp: '',
  proxy: '',
  created_from: '',
  created_to: '',
  q: '',
};

const bob: AccountSummary = {
  id: 7,
  run_id: 2,
  email: 'b@example.com',
  username: 'bob',
  created_at: '2026-03-01T10:00:00Z',
  proxy: 'p.example.com:8080',
  unconfirmed: true,
  totp: true,
};

const alice: AccountSummary = {
  id: 3,
  email: 'a@example.com',
  username: 'alice',
  created_at: '2026-01-01T10:00:00Z',
  totp: false,
};

const accounts: AccountPage = { accounts: [bob, alice], total: 5, next_before: 3 };

// The clock of the page reads this time, so each relative time is exact.
const now = Date.parse('2026-03-01T10:05:00Z');

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
  const url = new URL(`http://127.0.0.1/accounts${search}`);
  const event = { fetch, url } as unknown as Parameters<PageLoad>[0];
  return { requests, result: load(event) };
}

function renderPage(filters: Filters = none, page: AccountPage = accounts) {
  return render(Page, { data: { page, filters }, params: {} });
}

type Screen = Awaited<ReturnType<typeof renderPage>>;

function hrefs(links: Element[]): [string | null, string | null][] {
  return links.map((a) => [a.textContent, a.getAttribute('href')]);
}

function table(screen: Screen) {
  return screen.getByRole('table', { name: 'Accounts, newest first' });
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
  it('reads the accounts of the filters in the page query', async () => {
    const search = '?run=2&unconfirmed=&totp=true&proxy=&created_from=&created_to=&q=bob&before=9';
    const { requests, result } = loadWith(200, accounts, search);
    await expect(result).resolves.toEqual({
      page: accounts,
      filters: { ...none, run: '2', totp: 'true', q: 'bob' },
    });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname, new URL(r.url).search])).toEqual(
      [['GET', '/api/accounts', '?run=2&totp=true&q=bob&before=9']],
    );
  });

  it('sends a local time of the form as RFC 3339', async () => {
    const { requests, result } = loadWith(200, accounts, '?created_from=2026-02-15T10%3A30');
    await result;
    const sent = new URL(requests[0]?.url ?? '').searchParams.get('created_from');
    expect(sent).toBe(new Date(2026, 1, 15, 10, 30).toISOString());
  });

  it('reads the first page with no query', async () => {
    const { requests, result } = loadWith(200, accounts, '');
    await expect(result).resolves.toEqual({ page: accounts, filters: none });
    expect(new URL(requests[0]?.url ?? '').search).toBe('');
  });

  it('fails with the status of a refused read', async () => {
    const { result } = loadWith(400, { message: 'bad run' }, '?run=0');
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, 400)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Account read failed with HTTP 400' } });
  });
});

describe('table', () => {
  it('lists the summary of each account, with a link to its detail', async () => {
    const screen = await renderPage();
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Accounts');
    expect(cells(screen)).toEqual([
      ['ID', 'Created', 'Email', 'Username', 'Run', 'Proxy', '2FA', 'Status'],
      ['7', '5m ago', 'b@example.com', 'bob', '2', 'p.example.com:8080', 'On', 'Unconfirmed'],
      ['3', '1mo ago', 'a@example.com', 'alice', '', '', 'Off', ''],
    ]);
    expect(hrefs(table(screen).getByRole('link').elements())).toEqual([
      ['b@example.com', '/accounts/7'],
      ['a@example.com', '/accounts/3'],
    ]);
  });

  it('shows the created time as a time element with the full time', async () => {
    const screen = await renderPage();
    const times = [...table(screen).element().querySelectorAll('time')].map((t) => [
      t.getAttribute('datetime'),
      t.getAttribute('title'),
    ]);
    expect(times).toEqual([
      ['2026-03-01T10:00:00.000Z', 'Mar 1, 2026, 3:30:00 PM GMT+5:30'],
      ['2026-01-01T10:00:00.000Z', 'Jan 1, 2026, 3:30:00 PM GMT+5:30'],
    ]);
  });

  it('moves the relative times on with the clock of the page', async () => {
    const screen = await renderPage();
    const created = () => cells(screen)[1]?.[1];
    expect(created()).toBe('5m ago');
    vi.advanceTimersByTime(30_000 * 120);
    await expect.poll(created).toBe('1h ago');
  });

  it('shows 2FA and the status as badges, and nothing for a confirmed account', async () => {
    const screen = await renderPage();
    const badges = table(screen)
      .getByRole('row')
      .elements()
      .slice(1)
      .map((row) =>
        [...row.querySelectorAll('[data-tone]')].map((b) => [
          b.textContent.trim(),
          (b as HTMLElement).dataset.tone,
        ]),
      );
    expect(badges).toEqual([
      [
        ['On', 'success'],
        ['Unconfirmed', 'warning'],
      ],
      [['Off', 'muted']],
    ]);
  });

  it('keeps each cell on one line', async () => {
    const screen = await renderPage();
    for (const cell of table(screen).getByRole('cell').elements()) {
      expect(getComputedStyle(cell).whiteSpace).toBe('nowrap');
    }
  });

  it('shows the email link with no underline at rest, and with one on hover and on focus', async () => {
    const screen = await renderPage();
    const link = table(screen).getByRole('link', { name: 'b@example.com' });
    const line = () => getComputedStyle(link.element()).textDecorationLine;
    // The browser keeps the pointer between runs, so an earlier hover can sit on the link. Move it off.
    // Not with unhover(): it aims at the centre of the body, which can be a table cell on this page.
    await screen.getByRole('heading', { level: 1 }).hover();
    expect(line()).toBe('none');
    // The keyboard reaches the link, so the focus is visible.
    for (let tabs = 0; tabs < 30 && document.activeElement !== link.element(); tabs += 1) {
      await userEvent.tab();
    }
    expect(document.activeElement).toBe(link.element());
    expect(line()).toBe('underline');
    (link.element() as HTMLElement).blur();
    expect(line()).toBe('none');
    await link.hover();
    await expect.poll(line).toBe('underline');
  });

  it('hides the Run and the Proxy column when no account of the page has the field', async () => {
    const screen = await renderPage(none, { accounts: [alice], total: 1 });
    expect(cells(screen)).toEqual([
      ['ID', 'Created', 'Email', 'Username', '2FA', 'Status'],
      ['3', '1mo ago', 'a@example.com', 'alice', 'Off', ''],
    ]);
  });

  it('shows each optional column on its own', async () => {
    const runOnly = await renderPage(none, {
      accounts: [{ ...alice, run_id: 4 }],
      total: 1,
    });
    expect(cells(runOnly)[0]).toEqual([
      'ID',
      'Created',
      'Email',
      'Username',
      'Run',
      '2FA',
      'Status',
    ]);
    expect(cells(runOnly)[1]).toContain('4');
    await runOnly.unmount();

    const proxyOnly = await renderPage(none, {
      accounts: [{ ...alice, proxy: 'h.example.com:1' }],
      total: 1,
    });
    expect(cells(proxyOnly)).toEqual([
      ['ID', 'Created', 'Email', 'Username', 'Proxy', '2FA', 'Status'],
      ['3', '1mo ago', 'a@example.com', 'alice', 'h.example.com:1', 'Off', ''],
    ]);
  });
});

describe('total and empty states', () => {
  it('counts every account with no filter', async () => {
    const screen = await renderPage();
    expect(text(screen)).toContain('5 accounts Export');
  });

  it('counts the matching accounts, in the singular for one', async () => {
    const screen = await renderPage({ ...none, q: 'bob' }, { accounts: [bob], total: 1 });
    expect(text(screen)).toContain('1 matching account Export');
  });

  it('asks for a run when the store has no account', async () => {
    const screen = await renderPage(none, { accounts: [], total: 0 });
    await expect
      .element(screen.getByRole('link', { name: 'Start a run' }))
      .toHaveAttribute('href', '/');
    expect(text(screen)).toContain('No accounts yet. Start a run');
    expect(screen.getByRole('table').query()).toBeNull();
    expect(screen.getByRole('region', { name: 'Export' }).query()).toBeNull();
    expect(text(screen)).not.toContain('0 accounts');
  });

  it('offers to clear the filters when no account matches them', async () => {
    const screen = await renderPage({ ...none, totp: 'true' }, { accounts: [], total: 0 });
    await expect
      .element(screen.getByRole('link', { name: 'Clear the filters' }))
      .toHaveAttribute('href', '/accounts');
    expect(text(screen)).toContain('No account matches these filters. Clear the filters');
    expect(text(screen)).not.toContain('No accounts yet.');
    expect(screen.getByRole('table').query()).toBeNull();
    expect(screen.getByRole('region', { name: 'Export' }).query()).toBeNull();
  });

  it('shows only the total for an empty page of a filter with accounts', async () => {
    const screen = await renderPage(none, { accounts: [], total: 2 });
    expect(text(screen)).toContain('2 accounts Export');
    expect(screen.getByRole('table').query()).toBeNull();
    expect(text(screen)).not.toContain('No account');
  });
});

describe('links', () => {
  it('links the unconfirmed view, the clear and the next page', async () => {
    const screen = await renderPage({ ...none, totp: 'true', created_from: '2026-02-15T10:30' });
    await expect
      .element(screen.getByRole('link', { name: 'Unconfirmed accounts' }))
      .toHaveAttribute('href', '/accounts?unconfirmed=true');
    await expect
      .element(screen.getByRole('link', { name: 'Clear', exact: true }))
      .toHaveAttribute('href', '/accounts');
    await expect
      .element(screen.getByRole('link', { name: 'Next page' }))
      .toHaveAttribute('href', '/accounts?totp=true&created_from=2026-02-15T10%3A30&before=3');
  });

  it('has no next page link on the last page', async () => {
    const screen = await renderPage(none, { accounts: [alice], total: 1 });
    await expect.element(table(screen)).toBeVisible();
    expect(screen.getByRole('link', { name: 'Next page' }).query()).toBeNull();
  });

  it('exports the accounts of the filters in each format', async () => {
    const screen = await renderPage({ ...none, unconfirmed: 'true' });
    const section = screen.getByRole('region', { name: 'Export' });
    const links = section.getByRole('link').elements();
    expect(hrefs(links)).toEqual([
      ['JSONL', '/api/accounts/export?format=jsonl&unconfirmed=true'],
      ['CSV', '/api/accounts/export?format=csv&unconfirmed=true'],
      ['email:password', '/api/accounts/export?format=userpass&unconfirmed=true'],
    ]);
    for (const link of links) {
      expect(link.getAttribute('rel')).toBe('external');
      expect(link.hasAttribute('download')).toBe(true);
    }
  });
});

describe('filter form', () => {
  function form(screen: Screen): HTMLFormElement {
    return screen.getByRole('form', { name: 'Account filters' }).element() as HTMLFormElement;
  }

  function fold(screen: Screen): HTMLDetailsElement {
    const details = form(screen).querySelector('details');
    if (details === null) throw new Error('no More filters section');
    return details;
  }

  function summary(screen: Screen): HTMLElement {
    const shown = fold(screen).querySelector('summary');
    if (shown === null) throw new Error('no summary');
    return shown;
  }

  it('fills the form from the filters and names each field as the page query does', async () => {
    const filters: Filters = {
      run: '2',
      unconfirmed: 'true',
      totp: 'false',
      proxy: 'p.example.com:8080',
      created_from: '2026-02-15T10:30',
      created_to: '2026-07-01T00:05',
      q: 'bob',
    };
    const screen = await renderPage(filters);
    expect(form(screen).method).toBe('get');
    expect(Object.fromEntries(new FormData(form(screen)))).toEqual(filters);
    const types = ['Email or username', 'Run', 'Created from', 'Created before'].map(
      (label) => (screen.getByLabelText(label).element() as HTMLInputElement).type,
    );
    expect(types).toEqual(['search', 'number', 'datetime-local', 'datetime-local']);
    const choices = screen
      .getByLabelText('2FA')
      .getByRole('option')
      .elements()
      .map((o) => [(o as HTMLOptionElement).value, o.textContent]);
    expect(choices).toEqual([
      ['', 'Any'],
      ['true', 'Yes'],
      ['false', 'No'],
    ]);
    await expect
      .element(screen.getByRole('button', { name: 'Filter' }))
      .toHaveAttribute('type', 'submit');
  });

  it('keeps Run, Proxy and the dates in More filters, closed with none of them set', async () => {
    const screen = await renderPage({ ...none, unconfirmed: 'true', totp: 'true', q: 'bob' });
    const details = fold(screen);
    expect(summary(screen).textContent.trim()).toBe('More filters');
    expect(details.open).toBe(false);
    const inFold = [...details.querySelectorAll('input')].map((i) => i.name);
    expect(inFold).toEqual(['run', 'proxy', 'created_from', 'created_to']);
    await expect.element(screen.getByLabelText('Email or username')).toBeVisible();
    await expect.element(screen.getByLabelText('Run')).not.toBeVisible();
  });

  it.for(['run', 'proxy', 'created_from', 'created_to'] as const)(
    'opens More filters when %s is set',
    async (name) => {
      const screen = await renderPage({ ...none, [name]: '2' });
      expect(fold(screen).open).toBe(true);
    },
  );

  it('sends a value of More filters while the section is closed', async () => {
    const screen = await renderPage();
    summary(screen).click();
    await expect.element(screen.getByLabelText('Run')).toBeVisible();
    await screen.getByLabelText('Run').fill('2');
    summary(screen).click();
    await expect.element(screen.getByLabelText('Run')).not.toBeVisible();
    expect(fold(screen).open).toBe(false);

    let sent: [string, FormDataEntryValue][] = [];
    form(screen).addEventListener('submit', (e) => {
      // The test page must not navigate, so the test reads the entries that the submit would send.
      e.preventDefault();
      sent = [...new FormData(form(screen))];
    });
    await screen.getByRole('button', { name: 'Filter' }).click();
    expect(sent).toEqual([
      ['q', ''],
      ['unconfirmed', ''],
      ['totp', ''],
      ['run', '2'],
      ['proxy', ''],
      ['created_from', ''],
      ['created_to', ''],
    ]);
  });

  it('fits a phone width, and gives each date field room for its AM/PM', async () => {
    const screen = await renderPage({ ...none, created_from: '2026-02-15T10:30' });
    screen.container.style.width = '358px';
    const box = form(screen);
    await expect.poll(() => box.getBoundingClientRect().width).toBe(358);
    expect(box.scrollWidth).toBeLessThanOrEqual(box.clientWidth);
    for (const label of ['Created from', 'Created before']) {
      const input = screen.getByLabelText(label).element();
      expect(input.getBoundingClientRect().width).toBeGreaterThanOrEqual(233);
    }
  });
});
