import type { components } from '#lib/api.gen.d.ts';
import { dash, formatBytes, formatSeconds, percent, ratio } from '#lib/format.ts';

type RunState = components['schemas']['RunState'];
type RunCounts = components['schemas']['RunCounts'];

/** The text of each run phase. The run screen and the phase badge show it. */
export const phaseText: Record<RunState['phase'], string> = {
  idle: 'Idle',
  running: 'Running',
  stopping: 'Stopping, stage 1 of 2',
  stopping_now: 'Stopping now, stage 2 of 2',
  ended: 'Ended',
};

function failureTotal(counts: RunCounts): number {
  return Object.values(counts.failures).reduce((sum, n) => sum + n, 0);
}

/**
 * Returns the target of the run: the text "of 10" with the count as max, or
 * "No limit" with no max. It is undefined before the first run.
 */
export function accountTarget(state: RunState): { text: string; max?: number } | undefined {
  const count = state.account_count;
  if (count === undefined) {
    return undefined;
  }
  return count === -1 ? { text: 'No limit' } : { text: `of ${String(count)}`, max: count };
}

// The milliseconds from the start of the run to its end, or to now while it runs.
// A start after now gives 0. It is undefined before the first run.
function runSpan(state: RunState, now: Date): number | undefined {
  if (state.started_at === undefined) {
    return undefined;
  }
  const end = state.ended_at === undefined ? now.getTime() : Date.parse(state.ended_at);
  return Math.max(0, end - Date.parse(state.started_at));
}

/**
 * Returns the time from the start of the run to its end, or to now while it
 * runs, in the form of elapsedSince. It is a dash before the first run.
 */
export function runTime(state: RunState, now: Date): string {
  const span = runSpan(state, now);
  return span === undefined ? dash : formatSeconds(Math.floor(span / 1000));
}

/**
 * Returns the saved accounts per hour of the run time. It is a dash before the
 * first run, at a run time of 0 and for a start after now.
 */
export function accountsPerHour(state: RunState, now: Date): string {
  const span = runSpan(state, now);
  const perMs = span === undefined ? undefined : ratio(state.counts.accounts, span);
  return perMs === undefined ? dash : (perMs * 3_600_000).toFixed(1);
}

/** Returns the attempts with no failure out of all attempts, as a percent. It is a dash at 0 attempts. */
export function successRate(counts: RunCounts): string {
  return percent(counts.attempts - failureTotal(counts), counts.attempts);
}

/** Returns the failed attempts out of all attempts, for example "7 failed of 31 attempts". */
export function failedOfAttempts(counts: RunCounts): string {
  const attempts = counts.attempts === 1 ? 'attempt' : 'attempts';
  return `${String(failureTotal(counts))} failed of ${String(counts.attempts)} ${attempts}`;
}

/** Returns the failed attempts of each category, in the order of the category names. */
export function failuresByCategory(counts: RunCounts): { category: string; count: number }[] {
  return Object.entries(counts.failures)
    .map(([category, count]) => ({ category, count }))
    .sort((a, b) => a.category.localeCompare(b.category));
}

function browserBytes(counts: RunCounts): number {
  return counts.browser_bytes_sent + counts.browser_bytes_received;
}

function httpBytes(counts: RunCounts): number {
  return counts.http_bytes_sent + counts.http_bytes_received;
}

/**
 * Returns the four byte totals of the run per saved account. The bytes of the
 * failed attempts are part of it. It is a dash at 0 accounts.
 */
export function bytesPerAccount(counts: RunCounts): string {
  const perAccount = ratio(browserBytes(counts) + httpBytes(counts), counts.accounts);
  return perAccount === undefined ? dash : formatBytes(Math.round(perAccount));
}

/** Returns the byte totals of the run by phase, for example "23.3MiB browser, 3.3MiB HTTP in total". */
export function byteTotals(counts: RunCounts): string {
  return `${formatBytes(browserBytes(counts))} browser, ${formatBytes(httpBytes(counts))} HTTP in total`;
}
