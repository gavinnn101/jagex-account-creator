<script lang="ts">
  import { resolve } from '$app/paths';
  import { filterQuery } from '#lib/accounts/query.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import {
    accountsDigest,
    chromeDigest,
    generalDigest,
    mailboxDigest,
    runDigest,
  } from '#lib/digest.ts';
  import { formatTraffic, fullTime } from '#lib/format.ts';
  import { successRate } from '#lib/run/stats.ts';
  import { configEntries, failureText, runDuration, runStatus } from '#lib/runs/summary.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import Details from '#lib/ui/Details.svelte';
  import RelativeTime from '#lib/ui/RelativeTime.svelte';
  import Table from '#lib/ui/Table.svelte';
  import type { PageProps } from './$types';

  const { data }: PageProps = $props();
  const run = $derived(data.run);
  const status = $derived(runStatus(run) ?? ({ tone: 'neutral', label: 'Ended' } as const));
  // The relative times move on in steps of a minute, so a tick each 30 s is enough.
  const time = clock(30_000);

  // A phase with no byte gets no row: a run with no proxy counts none.
  const traffic = $derived(
    [
      ['Browser', run.counts.browser_bytes_sent, run.counts.browser_bytes_received] as const,
      ['HTTP', run.counts.http_bytes_sent, run.counts.http_bytes_received] as const,
    ].filter(([, sent, received]) => sent + received > 0),
  );

  const digests = $derived([
    ['Run settings', runDigest(run.config)],
    ['Mailbox', mailboxDigest(run.config)],
    ['Accounts', accountsDigest(run.config)],
    ['Chrome', chromeDigest(run.config)],
    ['General', generalDigest(run.config)],
  ]);
</script>

<p class="mb-gap"><a class="text-accent underline" href={resolve('/runs')}>All runs</a></p>

<header class="mb-section flex flex-wrap items-center gap-x-4 gap-y-2">
  <h1 class="text-2xl font-bold">Run {run.id}</h1>
  <Badge tone={status.tone}>{status.label}</Badge>
  <ul class="flex flex-wrap items-center gap-x-4 gap-y-1 sm:ml-auto">
    <li>
      <a
        class="text-accent underline"
        href={resolve(`/accounts?${filterQuery({ run: String(run.id) })}`)}>Accounts of this run</a
      >
    </li>
    <li>
      <!-- The path is an API file, not a route, so the router leaves it to the browser. -->
      <!-- Limitation: a run with no log file gets a 404. -->
      <a class="text-accent underline" href="/api/runs/{run.id}/log" rel="external" download
        >Download the log file</a
      >
    </li>
  </ul>
</header>

<div class="flex flex-col gap-section">
  {#if run.error !== undefined}
    <p class="break-words text-danger">Run error: {run.error}</p>
  {/if}

  <div class="grid items-start gap-section lg:grid-cols-3">
    <section aria-labelledby="run-timing" class="flex flex-col gap-gap">
      <h2 id="run-timing" class="text-lg font-semibold">Timing</h2>
      <dl class="term-grid gap-x-6 gap-y-2">
        <dt class="font-semibold">Source</dt>
        <dd>{run.source}</dd>
        <dt class="font-semibold">Started</dt>
        <dd>
          {fullTime(run.started_at)}
          <span class="text-muted">(<RelativeTime iso={run.started_at} now={time.now} />)</span>
        </dd>
        {#if run.ended_at !== undefined}
          <dt class="font-semibold">Ended</dt>
          <dd>
            {fullTime(run.ended_at)}
            <span class="text-muted">(<RelativeTime iso={run.ended_at} now={time.now} />)</span>
          </dd>
        {/if}
        <dt class="font-semibold">Duration</dt>
        <dd>{runDuration(run, time.now)}</dd>
      </dl>
    </section>

    <section aria-labelledby="run-results" class="flex flex-col gap-gap">
      <h2 id="run-results" class="text-lg font-semibold">Results</h2>
      <dl class="term-grid gap-x-6 gap-y-2">
        <dt class="font-semibold">Attempts</dt>
        <dd>{run.counts.attempts}</dd>
        <dt class="font-semibold">Accounts</dt>
        <dd>{run.counts.accounts}</dd>
        <dt class="font-semibold">Unconfirmed</dt>
        <dd>{run.counts.unconfirmed}</dd>
        <dt class="font-semibold">Success rate</dt>
        <dd>{successRate(run.counts)}</dd>
        <dt class="font-semibold">Failures</dt>
        <dd>{failureText(run.counts) || 'None'}</dd>
      </dl>
    </section>

    <section aria-labelledby="run-traffic" class="flex flex-col gap-gap">
      <h2 id="run-traffic" class="text-lg font-semibold">Traffic</h2>
      {#if traffic.length === 0}
        <p class="text-muted">None</p>
      {:else}
        <dl class="term-grid gap-x-6 gap-y-2">
          {#each traffic as [phase, sent, received] (phase)}
            <dt class="font-semibold">{phase}</dt>
            <dd>{formatTraffic(sent, received)}</dd>
          {/each}
        </dl>
      {/if}
    </section>
  </div>

  <section aria-labelledby="run-config" class="flex flex-col gap-gap">
    <h2 id="run-config" class="text-lg font-semibold">Config</h2>
    <dl class="term-grid gap-x-6 gap-y-2">
      {#each digests as [section, digest] (section)}
        <dt class="font-semibold">{section}</dt>
        <dd>{digest}</dd>
      {/each}
    </dl>
    <Details summary="All values">
      <Table caption="Config of the run" columns={['Key', 'Value']}>
        {#each configEntries(run.config) as [key, value] (key)}
          <tr>
            <td>{key}</td>
            <td>{value}</td>
          </tr>
        {/each}
      </Table>
    </Details>
  </section>
</div>
