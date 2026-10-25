import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import RunStatus from './RunStatus.svelte';

type RunState = components['schemas']['RunState'];
type Screen = Awaited<ReturnType<typeof render>>;

const now = new Date('2026-10-21T11:00:00.000Z');

function ago(seconds: number): string {
  return new Date(now.getTime() - seconds * 1000).toISOString();
}

function runState(over: Partial<RunState> = {}): RunState {
  return {
    phase: 'running',
    run_id: 7,
    started_at: ago(3600),
    account_count: 10,
    workers: [],
    cooldowns: [],
    counts: {
      attempts: 4,
      accounts: 3,
      unconfirmed: 1,
      failures: { timeout: 1 },
      browser_bytes_sent: 1024,
      browser_bytes_received: 2048,
      http_bytes_sent: 512,
      http_bytes_received: 3072,
    },
    ...over,
  };
}

const noFailures = { ...runState().counts, attempts: 3, failures: {} };

async function renderStatus(run: RunState | undefined, reconnecting = false) {
  const screen = await render(RunStatus, { run, reconnecting, now });
  await expect.element(screen.getByRole('heading', { name: 'Status' })).toBeVisible();
  return screen;
}

// Returns each card as its term and the text of each of its values.
function cards(screen: Screen): string[][] {
  return [...screen.container.querySelectorAll('dl > div')].map((card) =>
    [...card.children].map((child) => child.textContent.trim()),
  );
}

function rows(screen: Screen, caption: string): string[][] {
  const table = screen.getByRole('table', { name: caption }).element();
  return [...table.querySelectorAll('tbody tr')].map((tr) =>
    [...tr.querySelectorAll('td')].map((td) => td.textContent.replace(/\s+/g, ' ').trim()),
  );
}

function headers(screen: Screen, caption: string): string[] {
  const table = screen.getByRole('table', { name: caption }).element();
  return [...table.querySelectorAll('th')].map((th) => th.textContent);
}

function captions(screen: Screen): (string | undefined)[] {
  return [...screen.container.querySelectorAll('caption')].map((c) => c.textContent);
}

const noRun = 'No run yet. Check the config below, then press Start.';
const noWorker = 'No worker yet. The workers show after the start checks.';

describe('RunStatus', () => {
  it('waits for the first state', async () => {
    const screen = await renderStatus(undefined);
    await expect.element(screen.getByText('Connecting to the run state.')).toBeVisible();
    expect(screen.container.querySelector('dl')).toBeNull();
    expect(screen.container.querySelector('[role="status"]')).toBeNull();
  });

  it('marks the numbers stale while the stream reconnects', async () => {
    const screen = await renderStatus(runState(), true);
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('Reconnecting. The numbers below can be stale.');
  });

  it('shows one line in place of the numbers before the first run', async () => {
    const screen = await renderStatus({
      phase: 'idle',
      workers: [],
      cooldowns: [],
      counts: { ...noFailures, attempts: 0, accounts: 0, unconfirmed: 0 },
    });
    await expect.element(screen.getByText(noRun)).toBeVisible();
    expect(screen.container.querySelector('dl')).toBeNull();
    expect(captions(screen)).toEqual([]);
  });

  it('shows the counts of the run in four cards', async () => {
    const screen = await renderStatus(runState());
    expect(cards(screen)).toEqual([
      ['Accounts', '3', 'of 10', '1 unconfirmed'],
      ['Success rate', '75.0%', '1 failed of 4 attempts'],
      ['Accounts per hour', '3.0', 'over 1:00:00'],
      ['Bandwidth per account', '2.2KiB', '3.0KiB browser, 3.5KiB HTTP in total'],
    ]);
    const bar = screen.getByRole('progressbar', { name: 'Accounts of the target' });
    await expect.element(bar).toBeVisible();
    const progress = bar.element() as HTMLProgressElement;
    expect([progress.value, progress.max]).toEqual([3, 10]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
    expect(screen.getByText(noRun).query()).toBeNull();
  });

  it('shows no bar for a run with no limit', async () => {
    const screen = await renderStatus(runState({ account_count: -1 }));
    expect(cards(screen)[0]).toEqual(['Accounts', '3', 'No limit', '1 unconfirmed']);
    expect(screen.container.querySelector('progress')).toBeNull();
  });

  it('shows no target for a state with no account count', async () => {
    const state = runState();
    delete state.account_count;
    const screen = await renderStatus(state);
    expect(cards(screen)[0]).toEqual(['Accounts', '3', '1 unconfirmed']);
  });

  // A stop during the start checks gives a stopping run with no worker row.
  it.for(['running', 'stopping', 'stopping_now'] as const)(
    'says that the workers come after the start checks in phase %s',
    async (phase) => {
      const screen = await renderStatus(runState({ phase, counts: noFailures }));
      await expect.element(screen.getByText(noWorker)).toBeVisible();
      expect(captions(screen)).toEqual([]);
    },
  );

  it('shows no worker line and no workers table after the end', async () => {
    const workers = [{ worker: 0 }, { worker: 1 }];
    for (const w of [[], workers]) {
      const screen = await renderStatus(
        runState({ phase: 'ended', ended_at: ago(0), workers: w, counts: noFailures }),
      );
      expect(screen.getByText(noWorker).query()).toBeNull();
      expect(captions(screen)).toEqual([]);
      await screen.unmount();
    }
  });

  it('shows what each worker does and for how long', async () => {
    const workers: RunState['workers'] = [
      {
        worker: 0,
        attempt: {
          started_at: ago(30),
          proxy: 'proxy.example:8080',
          step: 'clearance',
          step_phase: 'browser',
          step_started_at: ago(5),
        },
      },
      {
        worker: 1,
        attempt: {
          started_at: ago(30),
          step: 'oauth_start',
          step_phase: 'http',
          step_started_at: ago(65),
        },
      },
      { worker: 2, wait: { kind: 'proxy', since: ago(12) } },
      { worker: 3, wait: { kind: 'backoff', since: ago(2), until: ago(-8) } },
      { worker: 4 },
    ];
    const screen = await renderStatus(runState({ workers }));
    expect(screen.getByText(noWorker).query()).toBeNull();
    const table = screen.getByRole('table', { name: 'Workers' }).element();
    expect(headers(screen, 'Workers')).toEqual(['Worker', 'Activity', 'Proxy', 'Time']);
    expect(rows(screen, 'Workers')).toEqual([
      ['0', 'browser clearance', 'proxy.example:8080', '0:05'],
      ['1', 'http oauth_start', '', '1:05'],
      ['2', 'Proxy wait every proxy is in cooldown', '', '0:12'],
      ['3', 'Backoff after a failed attempt', '', '0:02, ends in 0:08'],
      ['4', 'Idle', '', ''],
    ]);
    const tones = [...table.querySelectorAll('[data-tone]')].map((b) => [
      b.textContent,
      b.getAttribute('data-tone'),
    ]);
    expect(tones).toEqual([
      ['browser', 'accent'],
      ['http', 'accent'],
      ['Proxy wait', 'warning'],
      ['Backoff', 'warning'],
      ['Idle', 'muted'],
    ]);
  });

  it('hides the proxy column when no attempt has a proxy', async () => {
    const workers: RunState['workers'] = [
      {
        worker: 0,
        attempt: {
          started_at: ago(9),
          step: 'clearance',
          step_phase: 'browser',
          step_started_at: ago(9),
        },
      },
      { worker: 1, wait: { kind: 'proxy', since: ago(3) } },
    ];
    const screen = await renderStatus(runState({ workers }));
    expect(headers(screen, 'Workers')).toEqual(['Worker', 'Activity', 'Time']);
    expect(rows(screen, 'Workers')).toEqual([
      ['0', 'browser clearance', '0:09'],
      ['1', 'Proxy wait every proxy is in cooldown', '0:03'],
    ]);
  });

  it('shows each failure category in name order', async () => {
    const counts = { ...runState().counts, failures: { timeout: 2, challenge: 1, email: 3 } };
    const screen = await renderStatus(runState({ counts }));
    expect(captions(screen)).toEqual(['Failures by category']);
    expect(headers(screen, 'Failures by category')).toEqual(['Category', 'Failures']);
    expect(rows(screen, 'Failures by category')).toEqual([
      ['challenge', '1'],
      ['email', '3'],
      ['timeout', '2'],
    ]);
  });

  it('shows each proxy cooldown with its reason and its end', async () => {
    const cooldowns: RunState['cooldowns'] = [
      { proxy: 'a.example:1', reason: 'rate limited', until: ago(-90), failures: 2 },
      { proxy: 'b.example:2', reason: 'check failed', until: ago(-5), failures: 1 },
    ];
    const screen = await renderStatus(runState({ cooldowns, counts: noFailures }));
    expect(captions(screen)).toEqual(['Proxy cooldowns']);
    expect(headers(screen, 'Proxy cooldowns')).toEqual(['Proxy', 'Reason', 'Failures', 'Ends in']);
    expect(rows(screen, 'Proxy cooldowns')).toEqual([
      ['a.example:1', 'rate limited', '2', '1:30'],
      ['b.example:2', 'check failed', '1', '0:05'],
    ]);
  });
});
