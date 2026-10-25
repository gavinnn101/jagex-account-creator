import type { components } from '#lib/api.gen.d.ts';
import { elapsedSince } from '#lib/format.ts';
import { failuresByCategory } from '#lib/run/stats.ts';

type Run = components['schemas']['Run'];
type RunCounts = components['schemas']['RunCounts'];
type Config = components['schemas']['Config'];
type Values = Record<string, unknown>;

/** Returns the time from the start of the run to its end, or to now for a run with no end. */
export function runDuration(run: Run, now: Date): string {
  return elapsedSince(run.started_at, run.ended_at === undefined ? now : new Date(run.ended_at));
}

/** The status badge of a run. The title holds the run error. */
interface RunStatus {
  tone: 'accent' | 'danger';
  label: string;
  title?: string;
}

/** Returns the status badge of a run: "In progress" with no end, "Error" with an error, and undefined for a clean end. */
export function runStatus(run: Run): RunStatus | undefined {
  if (run.ended_at === undefined) {
    return { tone: 'accent', label: 'In progress' };
  }
  if (run.error !== undefined) {
    return { tone: 'danger', label: 'Error', title: run.error };
  }
  return undefined;
}

/** The optional columns of a page of runs. A column shows when a run of the page has a value for it. */
interface RunColumns {
  status: boolean;
  unconfirmed: boolean;
  failures: boolean;
}

/** Returns the optional columns that the runs of one page fill. */
export function runColumns(runs: Run[]): RunColumns {
  return {
    status: runs.some((run) => runStatus(run) !== undefined),
    unconfirmed: runs.some((run) => run.counts.unconfirmed > 0),
    failures: runs.some((run) => failureText(run.counts) !== ''),
  };
}

/** Returns the failed attempts of each category as one text, for example "email 2, proxy 1". */
export function failureText(counts: RunCounts): string {
  return failuresByCategory(counts)
    .map(({ category, count }) => `${category} ${String(count)}`)
    .join(', ');
}

// A section of the config is an object. Each other value is a scalar.
function entries(prefix: string, values: Values): [string, string][] {
  return Object.entries(values).flatMap(([key, value]): [string, string][] =>
    value instanceof Object
      ? entries(`${prefix}${key}.`, value as Values)
      : [[prefix + key, String(value)]],
  );
}

/**
 * Returns one [dotted key, value] row for each value of the config, in the order
 * of the object. The keys are the names of the API Config, so email.imap_pass_set
 * tells whether the run had an IMAP password.
 */
export function configEntries(config: Config): [string, string][] {
  return entries('', config);
}
