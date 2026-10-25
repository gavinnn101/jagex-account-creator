import type { components } from '#lib/api.gen.d.ts';
import { remainingUntil } from '#lib/format.ts';
import type { BadgeTone } from '#lib/ui/Badge.svelte';

type Proxy = components['schemas']['Proxy'];
type ProxyTest = components['schemas']['ProxyTest'];
type RunState = components['schemas']['RunState'];
type RunCooldown = components['schemas']['RunCooldown'];
type Phase = RunState['phase'];

/** The test of one proxy: in flight, answered, or a request that the server refused or never answered. */
export type TestState =
  | { kind: 'running' }
  | { kind: 'answered'; result: ProxyTest }
  | { kind: 'failed'; message: string };

/** The test state of a proxy as its cell shows it: a badge, and the error text when there is one. */
interface TestView {
  tone: BadgeTone;
  label: string;
  /** The error of a failed test or a failed request. Empty when there is none. */
  detail: string;
}

/** Returns the badge and the error text of the test state of a proxy. */
export function testView(state: TestState): TestView {
  switch (state.kind) {
    case 'running':
      return { tone: 'muted', label: 'Testing', detail: '' };
    case 'failed':
      return { tone: 'danger', label: 'Request failed', detail: state.message };
    case 'answered': {
      const { ok, error, duration_ms } = state.result;
      const time = `${String(duration_ms)} ms`;
      return ok
        ? { tone: 'success', label: `OK in ${time}`, detail: '' }
        : { tone: 'danger', label: `Failed in ${time}`, detail: error ?? '' };
    }
  }
}

/** The optional columns of the proxy table. Each one drives its header and its cells. */
interface ProxyColumns {
  /** Attempts, Success rate and Bytes. */
  health: boolean;
  lastAttempt: boolean;
  lastFailure: boolean;
  cooldown: boolean;
  test: boolean;
}

/**
 * Returns the optional columns that some listed proxy has a value for. The test
 * map can keep the entry of a deleted proxy, so the rule reads the listed ids.
 */
export function proxyColumns(
  proxies: readonly Proxy[],
  cooldowns: ReadonlyMap<string, RunCooldown>,
  tests: ReadonlyMap<number, TestState>,
): ProxyColumns {
  // Limitation: a cooldown or a test adds its column, so the columns move during a run or a test.
  // It fails toward a moving layout, never toward a hidden value.
  return {
    health: proxies.some((p) => p.attempts > 0),
    lastAttempt: proxies.some((p) => p.last_attempt_at !== undefined),
    lastFailure: proxies.some((p) => p.last_failure !== undefined),
    cooldown: proxies.some((p) => cooldowns.has(p.proxy)),
    test: proxies.some((p) => tests.has(p.id)),
  };
}

/** Returns the sum of the four byte counts of the attempts of p. */
export function totalBytes(p: Proxy): number {
  return (
    p.browser_bytes_sent + p.browser_bytes_received + p.http_bytes_sent + p.http_bytes_received
  );
}

// Each run keeps its own cooldowns, so a cooldown applies only while its run is in progress.
const inProgress: ReadonlySet<Phase> = new Set(['running', 'stopping', 'stopping_now']);

/** Returns the cooldowns of the run state by proxy, or none when no run is in progress. */
export function cooldownsByProxy(state: RunState | undefined): Map<string, RunCooldown> {
  if (state === undefined || !inProgress.has(state.phase)) {
    return new Map();
  }
  return new Map(state.cooldowns.map((c) => [c.proxy, c]));
}

/** Returns the text of a cooldown at now: its reason and the time to its end, as the run screen shows it. */
export function cooldownText(c: RunCooldown, now: Date): string {
  return `${c.reason}, ends in ${remainingUntil(c.until, now)}`;
}

/**
 * Returns true when the run phase changes to ended. The first phase of the
 * stream has no last phase, and the page has just read the list then.
 */
export function runEnded(last: Phase | undefined, next: Phase): boolean {
  return next === 'ended' && last !== undefined && last !== 'ended';
}
