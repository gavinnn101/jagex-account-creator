<script lang="ts">
  import { invalidate } from '$app/navigation';
  import { type ApiFailure, apiClient, errorMessages } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import Button from '#lib/ui/Button.svelte';
  import ConfirmButton from '#lib/ui/ConfirmButton.svelte';
  import ErrorList from '#lib/ui/ErrorList.svelte';
  import Field from '#lib/ui/Field.svelte';
  import Select from '#lib/ui/Select.svelte';

  type Config = components['schemas']['Config'];
  type Preset = components['schemas']['Preset'];

  interface Props {
    /** Every preset of the store, in name order. */
    presets: Preset[];
    /** The edited config. Save as preset sends it. */
    config: Config;
    /** Fills the form with the config of the loaded preset. */
    onload: (config: Config) => void;
    fetch: typeof globalThis.fetch;
  }

  const { presets, config, onload, fetch }: Props = $props();

  let selected: string | undefined = $state();
  let name = $state('');
  let saving = $state(false);
  let done = $state('');
  let problems: string[] = $state([]);

  const client = $derived(apiClient(fetch));
  // A selection that the list no longer holds falls back to the first preset.
  const current = $derived(presets.find((p) => String(p.id) === selected) ?? presets[0]);
  const options = $derived(presets.map((p) => ({ value: String(p.id), text: p.name })));

  // Sends one change, then reads the list again. A refused or failed change shows its errors.
  async function change(
    call: () => Promise<{ error?: ApiFailure }>,
    message: string,
  ): Promise<void> {
    problems = [];
    done = '';
    try {
      const { error } = await call();
      if (error === undefined) {
        await invalidate('/api/presets');
        done = message;
      } else {
        problems = errorMessages(error);
      }
    } catch (e) {
      problems = [String(e)];
    }
  }

  async function save(): Promise<void> {
    saving = true;
    await change(async () => {
      const answer = await client.POST('/api/presets', { body: { name, config } });
      if (answer.data !== undefined) {
        selected = String(answer.data.id);
      }
      return answer;
    }, 'Preset saved.');
    saving = false;
  }

  function remove(id: number): Promise<void> {
    return change(
      () => client.DELETE('/api/presets/{id}', { params: { path: { id } } }),
      'Preset deleted.',
    );
  }

  // The name follows the loaded preset, so a second save of it needs no typing.
  function load(preset: Preset): void {
    onload(preset.config);
    name = preset.name;
  }
</script>

<fieldset class="flex flex-col gap-gap">
  <legend class="mb-2 font-bold">Presets</legend>
  {#if current === undefined}
    <p class="text-sm text-muted">No preset yet.</p>
  {:else}
    <div class="flex flex-wrap items-end gap-gap">
      <Select
        label="Preset"
        {options}
        bind:value={
          () => String(current.id),
          (v: string | undefined) => {
            selected = v;
          }
        }
      />
      <Button
        variant="secondary"
        onclick={() => {
          load(current);
        }}>Load</Button
      >
      <!-- A confirm belongs to the preset that the operator selected at the Delete click. -->
      {#key current.id}
        <ConfirmButton name={current.name} onconfirm={() => remove(current.id)} />
      {/key}
    </div>
  {/if}
  <div class="flex flex-wrap items-end gap-gap">
    <Field label="Preset name" autocomplete="off" bind:value={name} />
    <Button variant="secondary" disabled={saving} onclick={save}>Save as preset</Button>
  </div>
  <ErrorList name="Preset errors" messages={problems} />
  {#if done !== ''}
    <p role="status">{done}</p>
  {/if}
</fieldset>
