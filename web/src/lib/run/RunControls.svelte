<script lang="ts">
  import { type ApiFailure, apiClient, errorMessages } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import Button from '#lib/ui/Button.svelte';
  import ErrorList from '#lib/ui/ErrorList.svelte';
  import { editedConfig } from './edited.ts';
  import Presets from './Presets.svelte';

  type Config = components['schemas']['Config'];
  type Phase = components['schemas']['RunState']['phase'];
  type Preset = components['schemas']['Preset'];

  interface Props {
    /** Every preset of the store. A load fills the config from one of them. */
    presets: Preset[];
    /** The phase of the run from the stream. It is undefined before the first event. */
    phase: Phase | undefined;
    /** The error that the run returned. */
    error: string | undefined;
    fetch: typeof globalThis.fetch;
  }

  const { presets, phase, error, fetch }: Props = $props();
  // Start sends the edited config, and a preset load fills it.
  const config = editedConfig();

  let starting = $state(false);
  let problems: string[] = $state([]);
  const stagesId = $props.id();

  const client = $derived(apiClient(fetch));
  const canStart = $derived(!starting && (phase === 'idle' || phase === 'ended'));
  const canStop = $derived(phase === 'running');
  const canStopNow = $derived(phase === 'running' || phase === 'stopping');

  // The run state comes from the stream, so send reads an answer only for its error.
  async function send(call: () => Promise<{ error?: ApiFailure }>): Promise<void> {
    problems = [];
    try {
      const answer = await call();
      if (answer.error !== undefined) {
        problems = errorMessages(answer.error);
      }
    } catch (e) {
      problems = [String(e)];
    }
  }

  async function start(): Promise<void> {
    starting = true;
    await send(() => client.POST('/api/run', { body: config }));
    starting = false;
  }

  // The loaded email has no imap_pass, so a typed password leaves the field and Start uses the password of serve.
  function load(loaded: Config): void {
    Object.assign(config, $state.snapshot(loaded));
  }
</script>

<div class="flex flex-wrap items-start justify-between gap-section">
  <div class="flex min-w-0 flex-col gap-2">
    <div class="flex flex-wrap items-center gap-gap">
      <Button variant="primary" disabled={!canStart} onclick={start}>Start</Button>
      <Button
        variant="secondary"
        disabled={!canStop}
        aria-describedby={stagesId}
        onclick={() => send(() => client.POST('/api/run/stop'))}>Stop</Button
      >
      <Button
        variant="danger"
        disabled={!canStopNow}
        aria-describedby={stagesId}
        onclick={() => send(() => client.POST('/api/run/stop-now'))}>Stop now</Button
      >
    </div>
    <p id={stagesId} class="max-w-prose text-sm text-muted">
      Stop: no new attempt starts, and an attempt that sent its password finishes. Stop now: it also
      ends that work and kills Chrome.
    </p>
    {#if error !== undefined}
      <p role="alert" aria-label="Run error" class="text-danger">Run error: {error}</p>
    {/if}
    <ErrorList name="Request errors" messages={problems} />
  </div>
  <Presets {presets} {config} onload={load} {fetch} />
</div>
