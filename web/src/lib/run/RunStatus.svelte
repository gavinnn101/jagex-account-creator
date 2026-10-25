<script lang="ts">
  import type { components } from '#lib/api.gen.d.ts';
  import { elapsedSince, remainingUntil } from '#lib/format.ts';
  import Badge, { type BadgeTone } from '#lib/ui/Badge.svelte';
  import Table from '#lib/ui/Table.svelte';
  import {
    accountTarget,
    accountsPerHour,
    byteTotals,
    bytesPerAccount,
    failedOfAttempts,
    failuresByCategory,
    runTime,
    successRate,
  } from './stats.ts';

  type RunState = components['schemas']['RunState'];
  type RunWorker = components['schemas']['RunWorker'];
  type WaitKind = NonNullable<RunWorker['wait']>['kind'];

  interface Activity {
    tone: BadgeTone;
    label: string;
    detail: string;
  }

  interface Props {
    /** The last run state of the stream. It is undefined before the first event. */
    run: RunState | undefined;
    /** True from a stream error until the next event. */
    reconnecting: boolean;
    /** The time of the last UI tick. */
    now: Date;
  }

  // Limitation: on a LAN listen address, the browser clock and the server clock can differ.
  const { run, reconnecting, now }: Props = $props();

  const waits: Record<WaitKind, Activity> = {
    proxy: { tone: 'warning', label: 'Proxy wait', detail: 'every proxy is in cooldown' },
    backoff: { tone: 'warning', label: 'Backoff', detail: 'after a failed attempt' },
  };

  function activity(worker: RunWorker): Activity {
    if (worker.attempt !== undefined) {
      return { tone: 'accent', label: worker.attempt.step_phase, detail: worker.attempt.step };
    }
    return worker.wait === undefined
      ? { tone: 'muted', label: 'Idle', detail: '' }
      : waits[worker.wait.kind];
  }

  // A backoff wait also shows its end. A proxy wait has no end, because it lasts until a cooldown ends.
  function timeIn(worker: RunWorker, at: Date): string {
    if (worker.attempt !== undefined) {
      return elapsedSince(worker.attempt.step_started_at, at);
    }
    if (worker.wait === undefined) {
      return '';
    }
    const since = elapsedSince(worker.wait.since, at);
    const until = worker.wait.until;
    return until === undefined ? since : `${since}, ends in ${remainingUntil(until, at)}`;
  }

  // Limitation: the column also hides while every worker waits for a proxy.
  // This fails toward a hidden column, never toward wrong data.
  function hasProxy(workers: RunWorker[]): boolean {
    return workers.some((w) => w.attempt?.proxy !== undefined);
  }

  function cards(state: RunState, at: Date): { term: string; value: string; detail: string }[] {
    const c = state.counts;
    return [
      { term: 'Success rate', value: successRate(c), detail: failedOfAttempts(c) },
      {
        term: 'Accounts per hour',
        value: accountsPerHour(state, at),
        detail: `over ${runTime(state, at)}`,
      },
      { term: 'Bandwidth per account', value: bytesPerAccount(c), detail: byteTotals(c) },
    ];
  }
</script>

<section aria-labelledby="run-status" class="flex flex-col gap-gap">
  <h2 id="run-status" class="text-lg font-semibold">Status</h2>

  {#if reconnecting}
    <p role="status" class="text-danger">Reconnecting. The numbers below can be stale.</p>
  {/if}

  {#if run === undefined}
    <p class="text-muted">Connecting to the run state.</p>
  {:else if run.phase === 'idle'}
    <p class="text-muted">No run yet. Check the config below, then press Start.</p>
  {:else}
    {@const target = accountTarget(run)}
    {@const failures = failuresByCategory(run.counts)}
    <dl class="grid grid-cols-2 gap-gap lg:grid-cols-4">
      <div class="flex min-w-0 flex-col gap-1 rounded-md border border-line bg-surface p-3">
        <dt class="text-sm text-muted">Accounts</dt>
        <dd class="text-2xl font-semibold tabular-nums">{run.counts.accounts}</dd>
        {#if target !== undefined}
          <dd class="flex flex-col gap-1 text-sm text-muted">
            {target.text}
            {#if target.max !== undefined}
              <progress
                aria-label="Accounts of the target"
                max={target.max}
                value={run.counts.accounts}
                class="h-1.5 w-full appearance-none overflow-hidden rounded-full bg-panel [&::-moz-progress-bar]:bg-accent [&::-webkit-progress-bar]:bg-panel [&::-webkit-progress-value]:bg-accent"
              ></progress>
            {/if}
          </dd>
        {/if}
        <dd class="text-sm text-muted tabular-nums">{run.counts.unconfirmed} unconfirmed</dd>
      </div>
      {#each cards(run, now) as card (card.term)}
        <div class="flex min-w-0 flex-col gap-1 rounded-md border border-line bg-surface p-3">
          <dt class="text-sm text-muted">{card.term}</dt>
          <dd class="text-2xl font-semibold tabular-nums">{card.value}</dd>
          <dd class="text-sm break-words text-muted tabular-nums">{card.detail}</dd>
        </div>
      {/each}
    </dl>

    {#if run.phase !== 'ended'}
      {#if run.workers.length === 0}
        <p class="text-muted">No worker yet. The workers show after the start checks.</p>
      {:else}
        {@const proxyColumn = hasProxy(run.workers)}
        <Table
          caption="Workers"
          columns={proxyColumn
            ? ['Worker', 'Activity', 'Proxy', 'Time']
            : ['Worker', 'Activity', 'Time']}
        >
          {#each run.workers as worker (worker.worker)}
            {@const a = activity(worker)}
            <tr>
              <td>{worker.worker}</td>
              <td>
                <span class="inline-flex flex-wrap items-baseline gap-x-2">
                  <Badge tone={a.tone}>{a.label}</Badge>
                  {a.detail}
                </span>
              </td>
              {#if proxyColumn}
                <td>{worker.attempt?.proxy ?? ''}</td>
              {/if}
              <td>{timeIn(worker, now)}</td>
            </tr>
          {/each}
        </Table>
      {/if}
    {/if}

    <div class="grid items-start gap-gap lg:grid-cols-2">
      {#if failures.length > 0}
        <Table caption="Failures by category" columns={['Category', 'Failures']}>
          {#each failures as failure (failure.category)}
            <tr>
              <td>{failure.category}</td>
              <td>{failure.count}</td>
            </tr>
          {/each}
        </Table>
      {/if}
      {#if run.cooldowns.length > 0}
        <Table caption="Proxy cooldowns" columns={['Proxy', 'Reason', 'Failures', 'Ends in']}>
          {#each run.cooldowns as cooldown (cooldown.proxy)}
            <tr>
              <td>{cooldown.proxy}</td>
              <td>{cooldown.reason}</td>
              <td>{cooldown.failures}</td>
              <td>{remainingUntil(cooldown.until, now)}</td>
            </tr>
          {/each}
        </Table>
      {/if}
    </div>
  {/if}
</section>
