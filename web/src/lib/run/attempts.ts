import { apiClient } from '#lib/api.ts';
import type { components } from '#lib/api.gen.d.ts';

type RunState = components['schemas']['RunState'];
type Attempt = components['schemas']['Attempt'];

/** One step of a cumulative series: the count at time, in milliseconds since the epoch. */
export interface SeriesPoint {
  time: number;
  count: number;
}

/** The two cumulative series of a run, each in time order. */
export interface RunSeries {
  /** One point at the end of each attempt that saved an account. */
  accounts: SeriesPoint[];
  /** One point at the end of each attempt that failed. */
  failures: SeriesPoint[];
}

interface PageQuery {
  limit: number;
  before?: number;
}

// The first sync of a run reads the most rows per page. A later sync reads only the few new rows.
const firstLimit = 500;
const laterLimit = 20;

function attemptEnd(a: Attempt): number {
  return Date.parse(a.started_at) + a.duration_ms;
}

function cumulative(ends: number[]): SeriesPoint[] {
  return ends.map((time, i) => ({ time, count: i + 1 }));
}

function buildSeries(rows: Iterable<Attempt>): RunSeries {
  const sorted = [...rows].sort((a, b) => attemptEnd(a) - attemptEnd(b));
  return {
    accounts: cumulative(sorted.filter((a) => a.account_id !== undefined).map(attemptEnd)),
    failures: cumulative(sorted.filter((a) => a.category !== undefined).map(attemptEnd)),
  };
}

/**
 * Holds the attempts of the current run and builds its two series. Pass each
 * run state to update. A change of the run id or of the attempt count queues a
 * sync over GET /api/runs/{id}/attempts. One sync runs at a time. A change
 * while a sync waits joins that sync, and a change while a sync runs queues
 * one more sync after it.
 *
 * A failed sync keeps the held rows. After a request that throws, the next
 * state syncs again. After a refused page, the next change of the count does.
 *
 * Limitation: after a refused page, the series misses the new rows until the
 * next change of the count.
 */
export class AttemptSync {
  readonly #client: ReturnType<typeof apiClient>;
  readonly #onSeries: (series: RunSeries) => void;
  #runId: number | undefined;
  #attempts: number | undefined;
  #rows = new Map<number, Attempt>();
  #tail = Promise.resolve();
  #queued = false;

  /** Sends each request through fetch, and passes the series to onSeries after each change. */
  constructor(fetch: typeof globalThis.fetch, onSeries: (series: RunSeries) => void) {
    this.#client = apiClient(fetch);
    this.#onSeries = onSeries;
  }

  /** Takes one run state from the stream. A new run id clears the held rows. */
  update(state: RunState): void {
    if (state.run_id !== this.#runId) {
      this.#runId = state.run_id;
      this.#attempts = undefined;
      this.#rows = new Map();
      this.#onSeries({ accounts: [], failures: [] });
    }
    if (state.counts.attempts === this.#attempts) {
      return;
    }
    this.#attempts = state.counts.attempts;
    if (this.#queued) {
      return;
    }
    this.#queued = true;
    // A request throws when the connection drops. The stream then reconnects, and
    // its first state can hold the same count, so that state must sync again.
    this.#tail = this.#tail
      .then(() => this.#runQueued())
      .catch(() => {
        this.#attempts = undefined;
      });
  }

  // Each queued sync reads the run id when it starts, so it covers each change before it.
  async #runQueued(): Promise<void> {
    this.#queued = false;
    // A state with no run id since the change leaves no run to sync.
    if (this.#runId !== undefined) {
      await this.#sync(this.#runId);
    }
  }

  async #sync(runId: number): Promise<void> {
    const rows = this.#rows;
    const limit = rows.size === 0 ? firstLimit : laterLimit;
    const found = await this.#fetchNew(runId, new Set(rows.keys()), { limit });
    // A new run during the fetch replaced rows, so the result belongs to no held run.
    if (found === undefined || found.length === 0 || rows !== this.#rows) {
      return;
    }
    for (const a of found) {
      rows.set(a.id, a);
    }
    this.#onSeries(buildSeries(rows.values()));
  }

  // Walks the pages, newest first, while every id on a page is new. A failed page gives undefined.
  async #fetchNew(
    runId: number,
    known: Set<number>,
    query: PageQuery,
  ): Promise<Attempt[] | undefined> {
    const { data } = await this.#client.GET('/api/runs/{id}/attempts', {
      params: { path: { id: runId }, query },
    });
    if (data === undefined) {
      return undefined;
    }
    const fresh = data.attempts.filter((a) => !known.has(a.id));
    if (fresh.length < data.attempts.length || data.next_before === undefined) {
      return fresh;
    }
    // The ids of this page are known from now on, so a page that comes again ends the walk.
    for (const a of fresh) {
      known.add(a.id);
    }
    // The last row of a page has its lowest id, the next_before. reduce throws on a page with no row.
    const before = fresh.reduce((_, a) => a).id;
    const older = await this.#fetchNew(runId, known, { limit: query.limit, before });
    return older === undefined ? undefined : [...fresh, ...older];
  }
}
