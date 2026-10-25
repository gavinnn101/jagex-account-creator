<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import type { components } from '#lib/api.gen.d.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import { AttemptSync, type RunSeries } from '#lib/run/attempts.ts';
  import { provideEditedConfig } from '#lib/run/edited.ts';
  import RunChart from '#lib/run/RunChart.svelte';
  import RunControls from '#lib/run/RunControls.svelte';
  import RunForm from '#lib/run/RunForm.svelte';
  import RunStatus from '#lib/run/RunStatus.svelte';
  import { runStream } from '#lib/run/stream.ts';
  import type { PageProps } from './$types';

  type RunState = components['schemas']['RunState'];

  const { data }: PageProps = $props();
  const stream = runStream();

  // The controls and the form share one edited copy of the saved config.
  // Limitation: a reload during a run resets the copy to the saved config.
  const config = $state(untrack(() => structuredClone(data.config)));
  provideEditedConfig(config);

  let run: RunState | undefined = $state();
  let reconnecting = $state(false);
  let series: RunSeries = $state({ accounts: [], failures: [] });
  // The tick redraws the times and sends no request.
  const time = clock(1000);

  // The chart runs from the start of the run to its end, or to now while it runs.
  function chartRange(state: RunState | undefined, at: Date) {
    if (state?.started_at === undefined) {
      return undefined;
    }
    const end = state.ended_at === undefined ? at : new Date(state.ended_at);
    return { start: new Date(state.started_at), end };
  }

  const range = $derived(chartRange(run, time.now));

  onMount(() => {
    const sync = new AttemptSync(fetch, (next) => {
      series = next;
    });
    return stream.subscribe({
      onMessage: (state) => {
        run = state;
        sync.update(state);
      },
      onReconnecting: (value) => {
        reconnecting = value;
      },
    });
  });
</script>

<h1 class="mb-gap text-2xl font-bold">Run</h1>

<div class="flex flex-col gap-section">
  <RunControls presets={data.presets} phase={run?.phase} error={run?.error} {fetch} />
  <RunStatus {run} {reconnecting} now={time.now} />
  {#if range !== undefined}
    <RunChart {series} start={range.start} end={range.end} />
  {/if}
  <RunForm {fetch} />
</div>
