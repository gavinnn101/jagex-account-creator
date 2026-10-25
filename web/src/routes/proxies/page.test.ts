import { isHttpError } from '@sveltejs/kit';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { invalidate } from '$app/navigation';
import type { components } from '#lib/api.gen.d.ts';
import { RunStream, runStreamKey } from '#lib/run/stream.ts';
import {
  type FakeEventSource,
  lastSource,
  runState,
  stubEventSource,
} from '#lib/streamtest/streamtest.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type Proxy = components['schemas']['Proxy'];
type RunCooldown = components['schemas']['RunCooldown'];

// The test page runs outside the SvelteKit router, so a real invalidate has no page to load.
vi.mock('$app/navigation', () => ({ invalidate: vi.fn(() => Promise.resolve()) }));

function proxy(id: number, over: Partial<Proxy> = {}): Proxy {
  return {
    id,
    proxy: `p${String(id)}.example:8080`,
    auth: false,
    enabled: true,
    added_at: '2026-10-22T10:00:00Z',
    attempts: 0,
    successes: 0,
    browser_bytes_sent: 0,
    browser_bytes_received: 0,
    http_bytes_sent: 0,
    http_bytes_received: 0,
    ...over,
  };
}

const used = proxy(1, {
  proxy: 'socks5://s.example:1080',
  auth: true,
  attempts: 3,
  successes: 2,
  browser_bytes_sent: 1024,
  browser_bytes_received: 1024,
  http_bytes_sent: 512,
  http_bytes_received: 512,
  last_attempt_at: '2026-10-22T11:00:00Z',
  last_failure: 'challenge',
});
const off = proxy(2, { enabled: false });

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.mocked(invalidate).mockClear();
});

// Answers each request with the reply for its method and path, and records each.
function stubFetch(reply: (sent: string) => Promise<Response>) {
  const sent: string[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const request = new Request(input);
    const line = `${request.method} ${new URL(request.url).pathname}`;
    sent.push(line);
    return reply(line);
  });
  return sent;
}

// The layout owns the stream, so the test opens it and passes it as the layout does.
async function renderPage(
  proxies: Proxy[] = [used, off],
  before: (source: FakeEventSource) => void = () => undefined,
) {
  stubEventSource();
  const stream = new RunStream();
  stream.open();
  const source = lastSource();
  before(source);
  const screen = await render(Page, {
    props: { data: { proxies }, params: {} },
    context: new Map([[runStreamKey, stream]]),
  });
  return { screen, source };
}

// The text of each cell, with each run of white space as one space, as the browser shows it.
function cells(screen: Awaited<ReturnType<typeof render>>): string[][] {
  return screen
    .getByRole('table', { name: 'Proxies' })
    .getByRole('row')
    .elements()
    .map((row) => [...row.children].map((cell) => cell.textContent.replace(/\s+/g, ' ').trim()));
}

// The text of the column of a row of the table, by the header of the column.
function cell(screen: Awaited<ReturnType<typeof render>>, row: number, column: string): string {
  const [head = [], ...rows] = cells(screen);
  return rows[row]?.[head.indexOf(column)] ?? '';
}

function loadWith(status: number, body: unknown) {
  const requests: Request[] = [];
  const fetch = (input: RequestInfo | URL): Promise<Response> => {
    requests.push(new Request(input));
    return Promise.resolve(Response.json(body, { status }));
  };
  const event = { fetch } as unknown as Parameters<PageLoad>[0];
  return { requests, result: load(event) };
}

describe('load', () => {
  it('reads the proxies', async () => {
    const { requests, result } = loadWith(200, { proxies: [used] });
    await expect(result).resolves.toEqual({ proxies: [used] });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname])).toEqual([
      ['GET', '/api/proxies'],
    ]);
  });

  it('fails with the status of a refused read', async () => {
    const { result } = loadWith(500, { message: 'boom' });
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, 500)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Proxy read failed with HTTP 500' } });
  });
});

// The lucide names of the icon of a button. An icon with an alias carries both names.
function icons(button: Element): string[] {
  return [...(button.querySelector('svg')?.classList ?? [])]
    .filter((c) => c.startsWith('lucide-') && c !== 'lucide-icon')
    .map((c) => c.slice('lucide-'.length));
}

// The cell of the column of a row of the table, by the header of the column.
function cellElement(
  screen: Awaited<ReturnType<typeof render>>,
  row: number,
  column: string,
): HTMLTableCellElement | undefined {
  const table = screen.getByRole('table', { name: 'Proxies' }).element() as HTMLTableElement;
  const head = [...(table.tHead?.rows[0]?.cells ?? [])].map((c) => c.textContent.trim());
  return table.tBodies[0]?.rows[row]?.cells[head.indexOf(column)];
}

// The text and the tone of each badge in a cell, in order.
function badges(screen: Awaited<ReturnType<typeof render>>, row: number, column: string): string[] {
  const td = cellElement(screen, row, column);
  return [...(td?.querySelectorAll('[data-tone]') ?? [])].map(
    (b) => `${b.textContent.trim()} ${String(b.getAttribute('data-tone'))}`,
  );
}

describe('page', () => {
  it('lists each proxy with its health and links its accounts', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-10-22T11:05:00Z') });
    const { screen } = await renderPage();
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Proxies');
    expect(cells(screen)).toEqual([
      ['Proxy', 'Attempts', 'Success rate', 'Bytes', 'Last attempt', 'Last failure', 'Actions'],
      ['socks5://s.example:1080 Auth', '3', '66.7%', '3.0KiB', '5m ago', 'challenge', ''],
      ['p2.example:8080 Disabled', '0', '—', '0B', '', '', ''],
    ]);
    expect(badges(screen, 0, 'Proxy')).toEqual(['Auth neutral']);
    expect(badges(screen, 1, 'Proxy')).toEqual(['Disabled muted']);
    const links = screen.getByRole('table').getByRole('link').elements();
    // The cell cuts a long host short with an ellipsis, and the title holds the whole host.
    expect(
      links.map((a) => [
        a.textContent,
        a.getAttribute('title'),
        a.getAttribute('href'),
        a.getAttribute('class'),
      ]),
    ).toEqual([
      [
        'socks5://s.example:1080',
        'socks5://s.example:1080',
        '/accounts?proxy=socks5%3A%2F%2Fs.example%3A1080',
        'max-w-64 truncate text-accent hover:underline focus-visible:underline',
      ],
      [
        'p2.example:8080',
        'p2.example:8080',
        '/accounts?proxy=p2.example%3A8080',
        'max-w-64 truncate text-accent hover:underline focus-visible:underline',
      ],
    ]);
    const rows = screen.getByRole('table').getByRole('row').elements().slice(1);
    expect(rows.map((r) => r.getAttribute('class'))).toEqual([
      'whitespace-nowrap',
      'whitespace-nowrap',
    ]);
    expect(screen.getByRole('table').element().getAttribute('data-sticky-actions')).toBe('true');
    expect(screen.container.textContent).not.toContain('No proxies yet.');
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('shows the last attempt as a relative time on the page clock', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval', 'Date'],
      now: Date.parse('2026-10-22T11:00:59Z'),
    });
    const { screen } = await renderPage();
    const shown = screen.getByRole('table').element().querySelector('time');
    expect(shown?.getAttribute('datetime')).toBe('2026-10-22T11:00:00.000Z');
    expect(shown?.textContent).toBe('now');
    vi.advanceTimersByTime(1000);
    await expect.poll(() => shown?.textContent).toBe('1m ago');
    expect(vi.getTimerCount()).toBe(1);
  });

  it('hides each optional column that no proxy has a value for', async () => {
    const { screen } = await renderPage([proxy(1, { auth: true }), proxy(2)]);
    await expect.element(screen.getByRole('table')).toBeVisible();
    expect(cells(screen)).toEqual([
      ['Proxy', 'Actions'],
      ['p1.example:8080 Auth', ''],
      ['p2.example:8080', ''],
    ]);
    expect(badges(screen, 1, 'Proxy')).toEqual([]);
  });

  it('shows the last failure column only when a proxy has one', async () => {
    const { screen } = await renderPage([proxy(1, { last_failure: 'challenge' }), proxy(2)]);
    await expect.element(screen.getByRole('table')).toBeVisible();
    expect(cells(screen)).toEqual([
      ['Proxy', 'Last failure', 'Actions'],
      ['p1.example:8080', 'challenge', ''],
      ['p2.example:8080', '', ''],
    ]);
  });

  it('counts the proxies and the enabled ones', async () => {
    const { screen } = await renderPage([used, off]);
    await expect
      .element(screen.getByText(/proxies, /))
      .toHaveTextContent('2 proxies, 1 enabled. A change applies at the next start of a run.');
    expect(screen.container.textContent).not.toContain('No proxy is enabled');
  });

  it('counts one proxy in the singular', async () => {
    const { screen } = await renderPage([used]);
    await expect
      .element(screen.getByText(/proxy, /))
      .toHaveTextContent('1 proxy, 1 enabled. A change applies at the next start of a run.');
  });

  it('warns when no proxy is enabled', async () => {
    const { screen } = await renderPage([off]);
    await expect
      .element(screen.getByText(/No proxy is enabled/))
      .toHaveTextContent(
        'No proxy is enabled, so a run stops at start unless "Direct, with no proxy" is on.',
      );
    expect(screen.getByText(/proxy, /).element()).toHaveTextContent(
      '1 proxy, 0 enabled. A change applies at the next start of a run.',
    );
  });

  it('says how to add a proxy to an empty list, and opens the import', async () => {
    const { screen } = await renderPage([]);
    await expect
      .element(screen.getByText('No proxies yet. Import a proxy file below.'))
      .toBeVisible();
    expect(screen.getByRole('table').query()).toBeNull();
    expect(screen.getByRole('button', { name: 'Test all enabled' }).query()).toBeNull();
    expect(screen.getByRole('link', { name: 'Export' }).query()).toBeNull();
    expect(screen.container.textContent).not.toContain('enabled.');
    expect(screen.container.textContent).not.toContain('No proxy is enabled');
    expect(screen.container.querySelector('details')?.open).toBe(true);
    await expect.element(screen.getByLabelText('Proxy lines')).toBeVisible();
  });

  it('closes the import while the list has proxies', async () => {
    const { screen } = await renderPage();
    const details = screen.container.querySelector('details');
    expect(details?.open).toBe(false);
    expect(details?.querySelector('summary')?.textContent.trim()).toBe('Import proxies');
    await expect.element(screen.getByLabelText('Proxy lines')).not.toBeVisible();
  });

  it('keeps the import open after the first import fills the list', async () => {
    const { screen } = await renderPage([]);
    await screen.rerender({ data: { proxies: [used] }, params: {} });
    await expect.element(screen.getByRole('table')).toBeVisible();
    expect(screen.container.querySelector('details')?.open).toBe(true);
  });

  it('shows the toggle icon and name of each state', async () => {
    const { screen } = await renderPage();
    const button = (name: string) => screen.getByRole('button', { name, exact: true }).element();
    const look = (name: string) => {
      const b = button(name) as HTMLElement;
      return [b.getAttribute('title'), b.dataset.variant, b.dataset.size, b.textContent.trim()];
    };
    await expect
      .element(screen.getByRole('button', { name: 'Disable socks5://s.example:1080' }))
      .toBeVisible();
    for (const name of [
      'Disable socks5://s.example:1080',
      'Enable p2.example:8080',
      'Test p2.example:8080',
      'Delete p2.example:8080',
    ]) {
      expect(look(name)).toEqual([name, 'ghost', 'icon', '']);
    }
    expect(icons(button('Disable socks5://s.example:1080'))).toContain('circle-pause');
    expect(icons(button('Disable socks5://s.example:1080'))).not.toContain('circle-play');
    expect(icons(button('Enable p2.example:8080'))).toContain('circle-play');
    expect(icons(button('Enable p2.example:8080'))).not.toContain('circle-pause');
    expect(icons(button('Test p2.example:8080'))).toContain('plug-zap');
    expect(icons(button('Delete p2.example:8080'))).toContain('trash');
  });

  it('links the export of the proxy file', async () => {
    const { screen } = await renderPage();
    await expect
      .element(screen.getByRole('button', { name: 'Test all enabled' }))
      .toHaveAttribute('data-variant', 'secondary');
    const link = screen.getByRole('link', { name: 'Export', exact: true });
    await expect.element(link).toHaveAttribute('href', '/api/proxies/export');
    await expect.element(link).toHaveAttribute('rel', 'external');
    await expect.element(link).toHaveAttribute('download');
  });

  it('disables and enables a proxy, then reads the list again', async () => {
    const sent = stubFetch(() => Promise.resolve(Response.json(used)));
    const { screen } = await renderPage();
    await screen.getByRole('button', { name: 'Disable socks5://s.example:1080' }).click();
    await expect.poll(() => vi.mocked(invalidate).mock.calls).toEqual([['/api/proxies']]);
    await screen.getByRole('button', { name: 'Enable p2.example:8080' }).click();
    await expect.poll(() => vi.mocked(invalidate).mock.calls).toHaveLength(2);
    expect(sent).toEqual(['POST /api/proxies/1/disable', 'POST /api/proxies/2/enable']);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('shows the error of a refused change and reads nothing', async () => {
    stubFetch(() =>
      Promise.resolve(Response.json({ message: 'proxy 2 not found' }, { status: 404 })),
    );
    const { screen } = await renderPage();
    await screen.getByRole('button', { name: 'Enable p2.example:8080' }).click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Change failed: proxy 2 not found');
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('shows a failed request, and clears it on the next change', async () => {
    let fail = true;
    stubFetch(() =>
      fail ? Promise.reject(new TypeError('network down')) : Promise.resolve(Response.json(off)),
    );
    const { screen } = await renderPage();
    const enable = screen.getByRole('button', { name: 'Enable p2.example:8080' });
    await enable.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Change failed: TypeError: network down');
    fail = false;
    await enable.click();
    await expect.poll(() => screen.container.querySelector('[role="alert"]')).toBeNull();
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/proxies');
  });

  it('deletes a proxy only after the confirm step', async () => {
    const sent = stubFetch(() => Promise.resolve(new Response(null, { status: 204 })));
    const { screen } = await renderPage();
    const del = (name: string) => screen.getByRole('button', { name, exact: true });
    await del('Delete p2.example:8080').click();
    await expect.element(del('Confirm delete p2.example:8080')).toBeVisible();
    expect(del('Confirm delete socks5://s.example:1080').query()).toBeNull();
    await del('Cancel delete p2.example:8080').click();
    await expect.element(del('Delete p2.example:8080')).toBeVisible();
    expect(del('Confirm delete p2.example:8080').query()).toBeNull();
    expect(sent).toEqual([]);

    await del('Delete p2.example:8080').click();
    await del('Confirm delete p2.example:8080').click();
    await expect.poll(() => vi.mocked(invalidate).mock.calls).toEqual([['/api/proxies']]);
    expect(sent).toEqual(['DELETE /api/proxies/2']);
    await expect.element(del('Delete p2.example:8080')).toBeVisible();
  });

  it('keeps the confirm of each row apart', async () => {
    const { screen } = await renderPage();
    const del = (name: string) => screen.getByRole('button', { name, exact: true });
    await del('Delete p2.example:8080').click();
    await del('Delete socks5://s.example:1080').click();
    await expect.element(del('Confirm delete socks5://s.example:1080')).toBeVisible();
    await expect.element(del('Confirm delete p2.example:8080')).toBeVisible();
  });

  it('deletes nothing on a double click on Delete', async () => {
    const sent = stubFetch(() => Promise.resolve(new Response(null, { status: 204 })));
    const { screen } = await renderPage();
    const del = (name: string) => screen.getByRole('button', { name, exact: true });
    await del('Delete p2.example:8080').dblClick();
    await expect.element(del('Delete p2.example:8080')).toBeVisible();
    expect(del('Confirm delete p2.example:8080').query()).toBeNull();
    expect(sent).toEqual([]);
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('tests one proxy and shows the answer', async () => {
    let answer: (r: Response) => void = () => undefined;
    const sent = stubFetch(
      () =>
        new Promise<Response>((done) => {
          answer = done;
        }),
    );
    const { screen } = await renderPage();
    const button = screen.getByRole('button', { name: 'Test p2.example:8080' });
    await button.click();
    await expect.element(button).toBeDisabled();
    expect(cell(screen, 1, 'Test')).toBe('Testing');
    expect(badges(screen, 1, 'Test')).toEqual(['Testing muted']);
    answer(Response.json({ ok: false, error: 'dial refused', duration_ms: 7 }));
    await expect.element(button).not.toBeDisabled();
    expect(cell(screen, 1, 'Test')).toBe('Failed in 7 ms dial refused');
    // The result shows next to the proxy, before the health columns.
    expect(cells(screen)).toEqual([
      [
        'Proxy',
        'Test',
        'Attempts',
        'Success rate',
        'Bytes',
        'Last attempt',
        'Last failure',
        'Actions',
      ],
      [
        'socks5://s.example:1080 Auth',
        '',
        '3',
        '66.7%',
        '3.0KiB',
        expect.any(String),
        'challenge',
        '',
      ],
      ['p2.example:8080 Disabled', 'Failed in 7 ms dial refused', '0', '—', '0B', '', '', ''],
    ]);
    expect(badges(screen, 1, 'Test')).toEqual(['Failed in 7 ms danger']);
    expect(cellElement(screen, 1, 'Test')?.querySelectorAll('span')).toHaveLength(3);
    expect(sent).toEqual(['POST /api/proxies/2/test']);
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('shows a refused or failed test request', async () => {
    let fail = false;
    stubFetch(() =>
      fail
        ? Promise.reject(new TypeError('network down'))
        : Promise.resolve(Response.json({ message: 'proxy 2 not found' }, { status: 404 })),
    );
    const { screen } = await renderPage();
    const button = screen.getByRole('button', { name: 'Test p2.example:8080' });
    await button.click();
    await expect.poll(() => cell(screen, 1, 'Test')).toBe('Request failed proxy 2 not found');
    expect(badges(screen, 1, 'Test')).toEqual(['Request failed danger']);
    fail = true;
    await button.click();
    await expect.poll(() => cell(screen, 1, 'Test')).toBe('Request failed TypeError: network down');
  });

  it('tests each enabled proxy, 4 at a time, and shows each answer as it comes', async () => {
    const answers = new Map<string, (r: Response) => void>();
    const sent = stubFetch(
      (line) =>
        new Promise<Response>((done) => {
          answers.set(line, done);
        }),
    );
    const list = [1, 2, 3, 4, 5, 6].map((id) => proxy(id));
    list.splice(2, 0, proxy(9, { enabled: false }));
    const { screen } = await renderPage(list);
    const all = screen.getByRole('button', { name: 'Test all enabled' });
    await all.click();
    await expect.element(all).toBeDisabled();
    await expect.poll(() => sent.length).toBe(4);
    expect(sent).toEqual([1, 2, 3, 4].map((id) => `POST /api/proxies/${String(id)}/test`));

    answers.get('POST /api/proxies/2/test')?.(Response.json({ ok: true, duration_ms: 5 }));
    await expect.poll(() => cell(screen, 1, 'Test')).toBe('OK in 5 ms');
    expect(badges(screen, 1, 'Test')).toEqual(['OK in 5 ms success']);
    // A passed test has no error text, so the cell holds only the badge in its wrapper.
    expect(cellElement(screen, 1, 'Test')?.querySelectorAll('span')).toHaveLength(2);
    await expect.poll(() => sent.length).toBe(5);
    expect(sent.at(-1)).toBe('POST /api/proxies/5/test');
    expect(cell(screen, 0, 'Test')).toBe('Testing');
    expect(cell(screen, 2, 'Test')).toBe('');

    for (const id of [1, 3, 4, 5]) {
      answers.get(`POST /api/proxies/${String(id)}/test`)?.(
        Response.json({ ok: true, duration_ms: id }),
      );
    }
    await expect.poll(() => sent.length).toBe(6);
    expect(sent.at(-1)).toBe('POST /api/proxies/6/test');
    await expect.element(all).toBeDisabled();
    answers.get('POST /api/proxies/6/test')?.(Response.json({ ok: true, duration_ms: 6 }));
    await expect.element(all).not.toBeDisabled();
    expect(sent).toHaveLength(6);
  });

  it('counts down the cooldown of a proxy while a run is in progress', async () => {
    vi.useFakeTimers({
      toFake: ['setInterval', 'clearInterval', 'Date'],
      now: Date.parse('2026-10-22T11:59:00Z'),
    });
    const { screen, source } = await renderPage();
    expect(source.url).toBe('/api/events');
    const cooldown: RunCooldown = {
      proxy: 'p2.example:8080',
      reason: 'rate limited',
      until: '2026-10-22T12:00:00Z',
      failures: 1,
    };
    source.emit(runState('running', { cooldowns: [cooldown] }));
    await expect.poll(() => cell(screen, 1, 'Cooldown')).toBe('rate limited, ends in 1:00');
    expect(badges(screen, 1, 'Cooldown')).toEqual(['rate limited, ends in 1:00 warning']);
    expect(cell(screen, 0, 'Cooldown')).toBe('');
    vi.advanceTimersByTime(999);
    // setTimeout is real here, so this lets any redraw run.
    await new Promise((done) => setTimeout(done, 0));
    expect(cell(screen, 1, 'Cooldown')).toBe('rate limited, ends in 1:00');
    vi.advanceTimersByTime(1);
    await expect.poll(() => cell(screen, 1, 'Cooldown')).toBe('rate limited, ends in 0:59');
    source.emit(runState('ended', { cooldowns: [cooldown] }));
    await expect.poll(() => cell(screen, 1, 'Cooldown')).toBe('');
  });

  it('reads the list again when a run ends', async () => {
    const { source } = await renderPage();
    source.emit(runState('ended'));
    source.emit(runState('running'));
    expect(invalidate).not.toHaveBeenCalled();
    source.emit(runState('stopping'));
    source.emit(runState('ended'));
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/proxies');
    source.emit(runState('ended'));
    expect(invalidate).toHaveBeenCalledOnce();
  });

  it('never shows the stale notice before a stream error, not even in the first render', async () => {
    const nodes: Node[] = [];
    const observer = new MutationObserver((records) => {
      for (const r of records) nodes.push(...r.addedNodes, ...r.removedNodes);
    });
    observer.observe(document.body, { subtree: true, childList: true });
    await renderPage();
    for (const r of observer.takeRecords()) nodes.push(...r.addedNodes, ...r.removedNodes);
    observer.disconnect();
    const status = nodes.filter(
      (n) => n instanceof Element && n.matches('[role="status"], :has([role="status"])'),
    );
    expect(nodes.length).toBeGreaterThan(0);
    expect(status).toEqual([]);
  });

  it('takes the state that the shared stream holds as its first state', async () => {
    const { screen, source } = await renderPage([used, off], (s) => {
      s.emit(runState('ended'));
      s.fail();
    });
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('Reconnecting. The cooldowns below can be stale.');
    // The page has just read the list, so the replayed end reads nothing.
    expect(invalidate).not.toHaveBeenCalled();
    source.emit(runState('running'));
    source.emit(runState('ended'));
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/proxies');
  });

  it('marks the cooldowns stale until the next event', async () => {
    const { screen, source } = await renderPage();
    const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
    expect(status()).toBeNull();
    // The fake stream calls the handler before it returns, so one tick shows each change.
    source.fail();
    await tick();
    expect(status()).toBe('Reconnecting. The cooldowns below can be stale.');
    source.emit(runState('idle'));
    await tick();
    expect(status()).toBeNull();
  });

  it('leaves the stream open, takes no more states and stops the tick on destroy', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    const { screen, source } = await renderPage();
    expect(vi.getTimerCount()).toBe(1);
    await screen.unmount();
    expect(vi.getTimerCount()).toBe(0);
    expect(source.closed).toBe(false);
    source.emit(runState('running'));
    source.emit(runState('ended'));
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('reads the list again after an import', async () => {
    stubFetch(() => Promise.resolve(Response.json({ added: 1, updated: 0, repeated: 0 })));
    const { screen } = await renderPage([]);
    const form = screen.getByRole('form', { name: 'Import proxies' });
    await form.getByLabelText('Proxy lines').fill('h.example:1');
    // A submit that the form lets through would reload the test runner, so the window stops it.
    const guard = (e: Event) => {
      e.preventDefault();
    };
    window.addEventListener('submit', guard);
    try {
      await form.getByRole('button', { name: 'Import' }).click();
      await expect.poll(() => vi.mocked(invalidate).mock.calls).toEqual([['/api/proxies']]);
    } finally {
      window.removeEventListener('submit', guard);
    }
  });
});
