import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { runState } from '#lib/streamtest/streamtest.ts';
import { AttemptSync, type RunSeries } from './attempts.ts';

type Attempt = components['schemas']['Attempt'];
type RunState = components['schemas']['RunState'];

const base = Date.parse('2026-10-21T10:00:00.000Z');
const empty: RunSeries = { accounts: [], failures: [] };

/** An attempt of 10 s that ends at base plus end seconds. */
function attempt(id: number, end: number, over: Partial<Attempt> = {}): Attempt {
  return {
    id,
    run_id: 1,
    worker: 0,
    started_at: new Date(base + (end - 10) * 1000).toISOString(),
    duration_ms: 10_000,
    steps: [],
    browser_bytes_sent: 0,
    browser_bytes_received: 0,
    http_bytes_sent: 0,
    http_bytes_received: 0,
    ...over,
  };
}

function saved(id: number, end: number): Attempt {
  return attempt(id, end, { account_id: id });
}

function failed(id: number, end: number): Attempt {
  return attempt(id, end, { category: 'challenge' });
}

function state(runId: number | undefined, attempts: number): RunState {
  const counts = { ...runState('running').counts, attempts };
  return runState('running', runId === undefined ? { counts } : { run_id: runId, counts });
}

function point(end: number, count: number) {
  return { time: base + end * 1000, count };
}

// The body read of a real Response ends after a later task, so a 0 ms timer cannot wait for it.
// This answer resolves its text at once, and the timer then runs after every step of the sync.
function reply(body: unknown, status: number): Response {
  const text = JSON.stringify(body);
  const answer = {
    status,
    ok: status === 200,
    headers: new Headers(),
    text: () => Promise.resolve(text),
  };
  return answer as unknown as Response;
}

async function settle(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

function harness() {
  const urls: string[] = [];
  const pending: { resolve: (r: Response) => void; reject: (e: unknown) => void }[] = [];
  const fetch = (input: RequestInfo | URL): Promise<Response> => {
    const url = new URL(new Request(input).url);
    urls.push(url.pathname + url.search);
    return new Promise((resolve, reject) => pending.push({ resolve, reject }));
  };
  const series: RunSeries[] = [];
  const sync = new AttemptSync(fetch, (s) => series.push(s));
  const next = () => {
    const p = pending.shift();
    if (p === undefined) throw new Error('no request in flight');
    return p;
  };
  const respond = async (body: unknown, status = 200) => {
    next().resolve(reply(body, status));
    await settle();
  };
  const fail = async () => {
    next().reject(new TypeError('Failed to fetch'));
    await settle();
  };
  const update = async (s: RunState) => {
    sync.update(s);
    await settle();
  };
  return { urls, series, update, respond, fail };
}

describe('AttemptSync', () => {
  it('builds both series from the rows of the run', async () => {
    const h = harness();
    await h.update(state(1, 4));
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500']);
    const unconfirmed = attempt(4, 40, { account_id: 4, category: 'unknown' });
    await h.respond({ attempts: [unconfirmed, failed(3, 20), saved(2, 30), saved(1, 10)] });
    expect(h.series).toEqual([
      empty,
      {
        accounts: [point(10, 1), point(30, 2), point(40, 3)],
        failures: [point(20, 1), point(40, 2)],
      },
    ]);
  });

  it('fetches nothing for a state with no run id', async () => {
    const h = harness();
    await h.update(state(undefined, 3));
    expect(h.urls).toEqual([]);
    expect(h.series).toEqual([]);
  });

  it('fetches nothing when the run id and the count stay the same', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.respond({ attempts: [saved(1, 10)] });
    await h.update(state(1, 1));
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500']);
  });

  it('reads a small page later and stops at a known id', async () => {
    const h = harness();
    await h.update(state(1, 2));
    await h.respond({ attempts: [saved(2, 20), saved(1, 10)] });
    await h.update(state(1, 3));
    await h.respond({ attempts: [failed(3, 30), saved(2, 20)], next_before: 2 });
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/1/attempts?limit=20']);
    expect(h.series.at(-1)).toEqual({
      accounts: [point(10, 1), point(20, 2)],
      failures: [point(30, 1)],
    });
  });

  it('walks the pages while every id on a page is new', async () => {
    const h = harness();
    await h.update(state(1, 3));
    await h.respond({ attempts: [saved(3, 30), saved(2, 20)], next_before: 2 });
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toEqual([
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=500&before=2',
    ]);
    expect(h.series).toEqual([
      empty,
      { accounts: [point(10, 1), point(20, 2), point(30, 3)], failures: [] },
    ]);
  });

  it('ends the walk on a page that comes again', async () => {
    const h = harness();
    await h.update(state(1, 2));
    const page = { attempts: [saved(2, 20), saved(1, 10)], next_before: 1 };
    await h.respond(page);
    await h.respond(page);
    expect(h.urls).toEqual([
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=500&before=1',
    ]);
    expect(h.series.at(-1)).toEqual({ accounts: [point(10, 1), point(20, 2)], failures: [] });
  });

  it('starts one more sync after changes during a sync', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.update(state(1, 2));
    await h.update(state(1, 3));
    expect(h.urls).toHaveLength(1);
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/1/attempts?limit=20']);
    await h.respond({ attempts: [saved(3, 30), saved(2, 20), saved(1, 10)] });
    expect(h.urls).toHaveLength(2);
    expect(h.series).toEqual([
      empty,
      { accounts: [point(10, 1)], failures: [] },
      { accounts: [point(10, 1), point(20, 2), point(30, 3)], failures: [] },
    ]);
  });

  it('passes no series when a sync finds no new row', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.respond({ attempts: [saved(1, 10)] });
    await h.update(state(1, 2));
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toHaveLength(2);
    expect(h.series).toHaveLength(2);
  });

  it('clears the rows on a new run id', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.respond({ attempts: [saved(1, 10)] });
    await h.update(state(2, 1));
    expect(h.series.at(-1)).toEqual(empty);
    await h.respond({ attempts: [failed(5, 50)] });
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/2/attempts?limit=500']);
    expect(h.series.at(-1)).toEqual({ accounts: [], failures: [point(50, 1)] });
  });

  it('syncs a new run that has the count of the last run', async () => {
    const h = harness();
    await h.update(state(1, 0));
    await h.respond({ attempts: [] });
    await h.update(state(2, 0));
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/2/attempts?limit=500']);
  });

  it('drops the rows of a run that changed during the fetch', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.update(state(2, 1));
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.series).toEqual([empty, empty]);
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/2/attempts?limit=500']);
  });

  it('fetches nothing after a state with no run id during a sync', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.update(state(2, 1));
    await h.update(state(undefined, 0));
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500']);
    expect(h.series).toEqual([empty, empty, empty]);
  });

  it('keeps the rows after a refused page, and the queued sync runs', async () => {
    const h = harness();
    await h.update(state(1, 2));
    await h.update(state(1, 3));
    await h.respond({ attempts: [saved(2, 20)], next_before: 2 });
    await h.respond({ message: 'boom' }, 500);
    expect(h.series).toEqual([empty]);
    await h.respond({ attempts: [saved(3, 30), saved(2, 20), saved(1, 10)] });
    expect(h.urls).toEqual([
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=500&before=2',
      '/api/runs/1/attempts?limit=500',
    ]);
    expect(h.series.at(-1)).toEqual({
      accounts: [point(10, 1), point(20, 2), point(30, 3)],
      failures: [],
    });
  });

  it('waits for a change of the count after a refused first page', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.respond({ message: 'boom' }, 500);
    await h.update(state(1, 1));
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500']);
    expect(h.series).toEqual([empty]);
  });

  it('waits for a change of the count after a refused later page', async () => {
    const h = harness();
    await h.update(state(1, 2));
    await h.respond({ attempts: [saved(2, 20)], next_before: 2 });
    await h.respond({ message: 'boom' }, 500);
    await h.update(state(1, 2));
    expect(h.urls).toEqual([
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=500&before=2',
    ]);
    expect(h.series).toEqual([empty]);
  });

  it('syncs the next state with the same count after a request that throws', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.fail();
    await h.update(state(1, 1));
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/1/attempts?limit=500']);
  });

  it('syncs again on the next change after a request that throws', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.fail();
    await h.update(state(1, 2));
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/1/attempts?limit=500']);
    expect(h.series.at(-1)).toEqual({ accounts: [point(10, 1)], failures: [] });
  });

  it('sends one request for each change after a thrown sync with a queued change', async () => {
    const h = harness();
    await h.update(state(1, 1));
    await h.update(state(1, 2));
    await h.fail();
    expect(h.urls).toEqual(['/api/runs/1/attempts?limit=500', '/api/runs/1/attempts?limit=500']);
    await h.respond({ attempts: [saved(1, 10)] });
    await h.update(state(1, 3));
    await h.respond({ attempts: [saved(1, 10)] });
    expect(h.urls).toEqual([
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=500',
      '/api/runs/1/attempts?limit=20',
    ]);
  });
});
