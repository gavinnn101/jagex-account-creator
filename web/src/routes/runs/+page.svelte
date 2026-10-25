<script lang="ts">
  import { resolve } from '$app/paths';
  import { filterQuery } from '#lib/accounts/query.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import { successRate } from '#lib/run/stats.ts';
  import { failureText, runColumns, runDuration, runStatus } from '#lib/runs/summary.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import RelativeTime from '#lib/ui/RelativeTime.svelte';
  import Table from '#lib/ui/Table.svelte';
  import type { PageProps } from './$types';

  const { data }: PageProps = $props();
  const runs = $derived(data.page.runs);
  const nextBefore = $derived(data.page.next_before);
  // The relative times move on in steps of a minute, so a tick each 30 s is enough.
  const time = clock(30_000);
  const runHref = resolve('/'); // mutation-ignore: StringLiteral: '' also resolves to /

  // Limitation: the rule reads one page, so a column can show on one page and hide on the next.
  const shown = $derived(runColumns(runs));
  const columns = $derived([
    'ID',
    'Started',
    'Duration',
    ...(shown.status ? ['Status'] : []),
    'Attempts',
    'Accounts',
    ...(shown.unconfirmed ? ['Unconfirmed'] : []),
    'Success rate',
    ...(shown.failures ? ['Failures'] : []),
    'Source',
  ]);
</script>

<h1 class="mb-section text-2xl font-bold">Runs</h1>

<div class="flex flex-col gap-section">
  {#if runs.length === 0}
    <p>
      No runs yet.
      <a class="text-accent underline" href={runHref}>Start a run</a>
    </p>
  {:else}
    <Table caption="Runs, newest first" {columns}>
      {#each runs as run (run.id)}
        {@const status = runStatus(run)}
        <tr class="whitespace-nowrap">
          <td>
            <a class="text-accent underline" href={resolve('/runs/[id]', { id: String(run.id) })}
              >{run.id}</a
            >
          </td>
          <td><RelativeTime iso={run.started_at} now={time.now} /></td>
          <td>{runDuration(run, time.now)}</td>
          {#if shown.status}
            <td>
              {#if status !== undefined}
                <span title={status.title}><Badge tone={status.tone}>{status.label}</Badge></span>
              {/if}
            </td>
          {/if}
          <td>{run.counts.attempts}</td>
          <td>
            <a
              class="text-accent underline"
              href={resolve(`/accounts?${filterQuery({ run: String(run.id) })}`)}
              title="Accounts of run {run.id}"
              aria-label="Accounts of run {run.id}: {run.counts.accounts}">{run.counts.accounts}</a
            >
          </td>
          {#if shown.unconfirmed}
            <td>{run.counts.unconfirmed}</td>
          {/if}
          <td>{successRate(run.counts)}</td>
          {#if shown.failures}
            <td>{failureText(run.counts)}</td>
          {/if}
          <td>{run.source}</td>
        </tr>
      {/each}
    </Table>
  {/if}

  {#if nextBefore !== undefined}
    <p>
      <a class="text-accent underline" href={resolve(`/runs?before=${String(nextBefore)}`)}
        >Next page</a
      >
    </p>
  {/if}
</div>
