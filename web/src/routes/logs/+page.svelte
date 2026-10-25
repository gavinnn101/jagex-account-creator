<script lang="ts">
  import { onMount } from 'svelte';
  import { resolve } from '$app/paths';
  import type { components } from '#lib/api.gen.d.ts';
  import { subscribe } from '#lib/events.ts';
  import { LogFeed } from '#lib/logs/feed.ts';
  import { attrValues, filterLines, levels, noFilter } from '#lib/logs/filters.ts';
  import LogList from '#lib/logs/LogList.svelte';
  import Button from '#lib/ui/Button.svelte';
  import Details from '#lib/ui/Details.svelte';
  import Field from '#lib/ui/Field.svelte';
  import Select from '#lib/ui/Select.svelte';

  type LogLine = components['schemas']['LogLine'];

  // The feed hands over a new array, so the lines need no deep proxy.
  let lines: LogLine[] = $state.raw([]);
  let reconnecting = $state(false);
  const filter = $state({ ...noFilter });

  const shown = $derived(filterLines(lines, filter));
  const first = $derived(lines[0]);
  const runHref = resolve('/'); // mutation-ignore: StringLiteral: '' also resolves to /
  const hasFilters = $derived(Object.values(filter).some((value) => value !== ''));
  const lineNoun = $derived(lines.length === 1 ? 'line' : 'lines');
  const count = $derived(
    hasFilters
      ? `${String(shown.length)} of ${String(lines.length)} ${lineNoun}`
      : `${String(lines.length)} ${lineNoun}`,
  );

  // The empty value selects every line.
  function choices(values: string[]) {
    return [{ value: '', text: 'Any' }, ...values.map((value) => ({ value, text: value }))];
  }

  const componentChoices = $derived(choices(attrValues(lines, 'component')));
  const workerChoices = $derived(choices(attrValues(lines, 'worker')));

  function clear(): void {
    Object.assign(filter, noFilter);
  }

  onMount(() => {
    const feed = new LogFeed((next) => {
      lines = next;
    });
    return subscribe<LogLine>('/api/logs', {
      onMessage: (line) => {
        feed.add(line);
      },
      onReconnecting: (value) => {
        reconnecting = value;
      },
    });
  });
</script>

<div class="mb-section flex flex-wrap items-center justify-between gap-gap">
  <h1 class="text-2xl font-bold">Logs</h1>
  {#if first !== undefined}
    <!-- The path is an API file, not a route, so the router leaves it to the browser. -->
    <a class="text-accent underline" href="/api/runs/{first.run_id}/log" rel="external" download
      >Download the log file</a
    >
  {/if}
</div>

<div class="flex flex-col gap-section">
  {#if reconnecting}
    <p role="status" class="text-danger">Reconnecting. The lines below can be incomplete.</p>
  {/if}

  {#if first === undefined}
    <p>
      No run log yet. <a class="text-accent underline" href={runHref}>Start a run</a>, or get a past
      log from <a class="text-accent underline" href={resolve('/runs')}>Runs</a>.
    </p>
  {:else}
    <p class="flex flex-wrap gap-x-3 tabular-nums">
      <a class="text-accent underline" href={resolve('/runs/[id]', { id: String(first.run_id) })}
        >Run {first.run_id}</a
      >
      <span>{count}</span>
    </p>

    <!-- No button submits, so the form never sends itself. The filters start empty when the
         section first shows, so it starts closed. -->
    <form aria-label="Log filters" class="flex flex-col gap-gap">
      <div class="flex flex-wrap items-end gap-gap">
        <div class="w-28">
          <Select label="Level" options={choices(levels)} bind:value={filter.level} />
        </div>
        <div class="min-w-0 flex-[1_1_12rem]">
          <Field label="Text" bind:value={filter.text} />
        </div>
        {#if hasFilters}
          <Button variant="secondary" onclick={clear}>Clear</Button>
        {/if}
      </div>
      <Details summary="More filters">
        <div class="grid gap-gap sm:grid-cols-2 xl:grid-cols-4">
          <Select label="Component" options={componentChoices} bind:value={filter.component} />
          <Select label="Worker" options={workerChoices} bind:value={filter.worker} />
          <Field label="Account" inputmode="numeric" bind:value={filter.account} />
          <Field label="Email" bind:value={filter.email} />
        </div>
      </Details>
    </form>

    {#if shown.length === 0}
      <p>
        No line matches these filters.
        <Button variant="ghost" onclick={clear}>Clear the filters</Button>
      </p>
    {:else}
      <LogList lines={shown} />
    {/if}
  {/if}
</div>
