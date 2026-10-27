import '../app.css';
import { isHttpError } from '@sveltejs/kit';
import { tick } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import { chartGeometry } from '#lib/run/chart.ts';
import { RunStream, runStreamKey } from '#lib/run/stream.ts';
import {
  type FakeEventSource,
  lastSource,
  runState as idleState,
  stubEventSource,
} from '#lib/streamtest/streamtest.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type Config = components['schemas']['Config'];
type RunState = components['schemas']['RunState'];
type Attempt = components['schemas']['Attempt'];
type Preset = components['schemas']['Preset'];

const config: Config = {
  debug: false,
  database: 'creator.db',
  chrome: { headless: true, timeout: '30s' },
  proxy: { direct: false },
  email: {
    imap_host: '',
    imap_port: 993,
    imap_user: '',
    imap_pass_set: false,
    imap_domains: [],
    auth: 'password',
    oauth2: { client_id: '', client_secret_set: false, refresh_token_set: false, token_url: '' },
  },
  account: { password_length: 16, enable_totp: true },
  concurrency: { max_workers: 1, max_browsers: 1, account_count: 1, account_timeout: '1m' },
};

const preset: Preset = {
  id: 3,
  name: 'Fast',
  saved_at: '2026-10-22T10:00:00Z',
  config: { ...config, concurrency: { ...config.concurrency, max_workers: 4 } },
};

const started = Date.parse('2026-10-21T10:00:00.000Z');

function iso(seconds: number): string {
  return new Date(started + seconds * 1000).toISOString();
}

// A state of run 1, started at iso(0), with one saved account of one.
function runState(phase: RunState['phase'], over: Partial<RunState> = {}): RunState {
  const zero = idleState(phase).counts;
  return idleState(phase, {
    run_id: 1,
    started_at: iso(0),
    account_count: 1,
    counts: { ...zero, attempts: 1, accounts: 1 },
    ...over,
  });
}

const saved: Attempt = {
  id: 1,
  run_id: 1,
  worker: 0,
  account_id: 1,
  started_at: iso(10),
  duration_ms: 20_000,
  steps: [],
  browser_bytes_sent: 0,
  browser_bytes_received: 0,
  http_bytes_sent: 0,
  http_bytes_received: 0,
};

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// Renders the page on a fake clock at started plus 60 s, with a fake stream and a fake API.
// The layout owns the stream, so the test opens it and passes it as the layout does.
async function renderPage(before: (source: FakeEventSource) => void = () => undefined) {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], now: started + 60_000 });
  stubEventSource();
  const paths: string[] = [];
  const requests: Request[] = [];
  vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
    const request = new Request(input);
    requests.push(request);
    paths.push(new URL(request.url).pathname);
    return Promise.resolve(Response.json({ attempts: [saved] }));
  });
  const stream = new RunStream();
  stream.open();
  const source = lastSource();
  before(source);
  const screen = await render(Page, {
    props: { data: { config, presets: [preset] }, params: {} },
    context: new Map([[runStreamKey, stream]]),
  });
  const button = (name: string) => screen.getByRole('button', { name, exact: true });
  const enabled = () =>
    ['Start', 'Stop', 'Stop now'].map(
      (name) => !(button(name).element() as HTMLButtonElement).disabled,
    );
  return { screen, source, paths, requests, button, enabled };
}

// Answers each read with the status and the body of its path, after release.
function loadWith(replies: Record<string, [number, unknown]>) {
  const requests: Request[] = [];
  let release: () => void = () => undefined;
  const released = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fetch = async (input: RequestInfo | URL): Promise<Response> => {
    const request = new Request(input);
    requests.push(request);
    await released;
    const [status, body] = replies[new URL(request.url).pathname] ?? [404, { message: 'none' }];
    return Response.json(body, { status });
  };
  const event = { fetch } as Parameters<PageLoad>[0];
  return { requests, release, result: load(event) };
}

describe('load', () => {
  it('reads the config and the presets of serve together', async () => {
    const { requests, release, result } = loadWith({
      '/api/config': [200, config],
      '/api/presets': [200, { presets: [preset] }],
    });
    // Both requests leave before either answer comes.
    await expect.poll(() => requests.length).toBe(2);
    release();
    await expect(result).resolves.toEqual({ config, presets: [preset] });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname])).toEqual([
      ['GET', '/api/config'],
      ['GET', '/api/presets'],
    ]);
  });

  it.for<[string, number, string]>([
    ['/api/config', 500, 'Config read failed with HTTP 500'],
    ['/api/presets', 503, 'Preset read failed with HTTP 503'],
  ])('fails with the status of a refused read of %s', async ([path, status, message]) => {
    const replies: Record<string, [number, unknown]> = {
      '/api/config': [200, config],
      '/api/presets': [200, { presets: [] }],
    };
    replies[path] = [status, { message: 'boom' }];
    const { release, result } = loadWith(replies);
    release();
    const err: unknown = await Promise.resolve(result).catch((e: unknown) => e);
    expect(isHttpError(err, status)).toBe(true);
    expect(err).toMatchObject({ body: { message } });
  });
});

describe('page', () => {
  it('turns the buttons on by the phase of the stream', async () => {
    const { screen, source, enabled } = await renderPage();
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Run');
    await expect.element(screen.getByRole('combobox', { name: 'Preset' })).toHaveValue('3');
    expect(source.url).toBe('/api/events');
    expect(enabled()).toEqual([false, false, false]);
    await expect.element(screen.getByText('Connecting to the run state.')).toBeVisible();
    expect(screen.container.querySelector('[role="status"]')).toBeNull();
    const steps: [RunState['phase'], boolean[]][] = [
      ['idle', [true, false, false]],
      ['running', [false, true, true]],
      ['stopping', [false, false, true]],
      ['stopping_now', [false, false, false]],
      ['ended', [true, false, false]],
    ];
    for (const [phase, want] of steps) {
      source.emit(runState(phase));
      await expect.poll(enabled).toEqual(want);
    }
  });

  it('puts the controls first and the config last', async () => {
    const { screen, source, button } = await renderPage();
    source.emit(runState('running'));
    const chart = screen.getByRole('img', { name: /^Saved accounts/ });
    await expect.element(chart).toBeVisible();
    const order = [
      button('Start').element(),
      screen.getByRole('heading', { name: 'Status' }).element(),
      chart.element(),
      screen.getByRole('form', { name: 'Run config' }).element(),
    ];
    for (const [i, el] of order.slice(1).entries()) {
      expect(order[i]?.compareDocumentPosition(el)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    }
  });

  it('shares one edited config between the controls and the form', async () => {
    const { screen, source, requests, button } = await renderPage();
    source.emit(idleState('idle'));
    const workers = screen.getByLabelText('Max workers');
    await expect.element(workers).toHaveValue(1);
    await button('Load').click();
    await expect.element(workers).toHaveValue(4);
    await workers.fill('6');
    await button('Start').click();
    await expect.poll(() => requests.length).toBe(1);
    const sent = requests[0];
    expect([sent?.method, sent === undefined ? '' : new URL(sent.url).pathname]).toEqual([
      'POST',
      '/api/run',
    ]);
    expect(await sent?.text()).toBe(
      JSON.stringify({ ...config, concurrency: { ...config.concurrency, max_workers: 6 } }),
    );
    // The page edits a copy, so the loaded data keeps its values.
    expect(config.concurrency.max_workers).toBe(1);
    expect(preset.config.concurrency.max_workers).toBe(4);
  });

  it('shows the error of an ended run beside the controls', async () => {
    const { screen, source } = await renderPage();
    source.emit(runState('ended', { ended_at: iso(30), error: 'imap login: EOF' }));
    await expect
      .element(screen.getByRole('alert', { name: 'Run error' }))
      .toHaveTextContent('Run error: imap login: EOF');
    const status = screen.getByRole('region', { name: 'Status' }).element();
    expect(status.querySelector('[role="alert"]')).toBeNull();
  });

  it('marks the stream stale until the next event', async () => {
    const { screen, source } = await renderPage();
    const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
    // The fake stream calls the handler before it returns, so one tick shows each change.
    source.emit(runState('running'));
    source.fail();
    await tick();
    expect(status()).toBe('Reconnecting. The numbers below can be stale.');
    source.emit(runState('running'));
    await tick();
    expect(status()).toBeNull();
  });

  it('redraws the time in step on each tick', async () => {
    const { screen, source } = await renderPage();
    const attempt = {
      started_at: iso(50),
      step: 'clearance',
      step_phase: 'browser',
      step_started_at: iso(55),
    };
    source.emit(runState('running', { workers: [{ worker: 0, attempt }] }));
    const cell = screen.getByRole('cell', { name: '0:05' });
    await expect.element(cell).toBeVisible();
    vi.advanceTimersByTime(1000);
    await expect.element(screen.getByRole('cell', { name: '0:06' })).toBeVisible();
  });

  it('charts a running run up to now and an ended run up to its end', async () => {
    const { screen, source, paths } = await renderPage();
    // The chart card is 642 px wide, so its plot area is 544 by 200 px.
    screen.container.style.width = '642px';
    const idle = runState('idle');
    delete idle.run_id;
    delete idle.started_at;
    source.emit(idle);
    await expect
      .element(screen.getByText('No run yet. Check the config below, then press Start.'))
      .toBeVisible();
    expect(screen.container.querySelector('svg[role="img"]')).toBeNull();

    // The first state of a run can come before its run row, so it has no run id.
    const unrecorded = runState('running');
    delete unrecorded.run_id;
    source.emit(unrecorded);
    const none = { accounts: [], failures: [] };
    const empty = chartGeometry(none, new Date(started), new Date(started + 60_000), 544, 200);
    const img = screen.getByRole('img', { name: 'Saved accounts: 0. Failed attempts: 0.' });
    await expect.element(img).toBeVisible();
    const drawn = [...img.element().querySelectorAll('path')].map((p) => p.getAttribute('d'));
    expect(drawn).toEqual([empty.failures, empty.accounts]);
    expect(paths).toEqual([]);

    source.emit(runState('running'));
    const series = { accounts: [{ time: started + 30_000, count: 1 }], failures: [] };
    const path = () =>
      screen.container.querySelector('svg[role="img"] path:last-of-type')?.getAttribute('d');
    const live = chartGeometry(series, new Date(started), new Date(started + 60_000), 544, 200);
    await expect.poll(path).toBe(live.accounts);
    expect(paths).toEqual(['/api/runs/1/attempts']);

    source.emit(runState('ended', { ended_at: iso(40) }));
    const ended = chartGeometry(series, new Date(started), new Date(started + 40_000), 544, 200);
    await expect.poll(path).toBe(ended.accounts);
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

  it('shows the state that the shared stream holds at once', async () => {
    const { screen, enabled } = await renderPage((source) => {
      source.emit(runState('running'));
      source.fail();
    });
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('Reconnecting. The numbers below can be stale.');
    expect(enabled()).toEqual([false, true, true]);
  });

  it('leaves the stream open, takes no more states and stops the tick on destroy', async () => {
    const { screen, source, paths } = await renderPage();
    expect(vi.getTimerCount()).toBe(1);
    await screen.unmount();
    expect(vi.getTimerCount()).toBe(0);
    expect(source.closed).toBe(false);
    source.emit(runState('running'));
    // setTimeout is real here, so this lets any sync request leave.
    await new Promise((done) => setTimeout(done, 0));
    expect(paths).toEqual([]);
  });
});
