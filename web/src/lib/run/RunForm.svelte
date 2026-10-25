<script lang="ts">
  import { apiClient, errorMessages } from '#lib/api.ts';
  import ConfigFields from '#lib/ConfigFields.svelte';
  import Button from '#lib/ui/Button.svelte';
  import ErrorList from '#lib/ui/ErrorList.svelte';
  import { editedConfig } from './edited.ts';

  interface Props {
    fetch: typeof globalThis.fetch;
  }

  const { fetch }: Props = $props();
  // Save sends the edited config and writes the saved email section back into it.
  const config = editedConfig();

  let saving = $state(false);
  let saved = $state(false);
  let problems: string[] = $state([]);

  // A run keeps the config that it started with, so a save during a run is safe.
  async function save(): Promise<void> {
    saving = true;
    saved = false;
    problems = [];
    try {
      const { data, error } = await apiClient(fetch).PUT('/api/config', { body: config });
      if (error !== undefined) {
        problems = errorMessages(error);
      } else {
        // The saved email has no imap_pass, so the typed password leaves the field
        // and the placeholder follows serve.
        config.email = data.email;
        saved = true;
      }
    } catch (e) {
      problems = [String(e)];
    }
    saving = false;
  }
</script>

<!-- No button submits, so the form never sends itself. -->
<form class="flex flex-col gap-gap" aria-label="Run config">
  <div class="flex flex-wrap items-center gap-gap">
    <h2 class="text-lg font-semibold">Config</h2>
    <div class="ml-auto flex flex-wrap items-center gap-gap">
      {#if saved}
        <p role="status" class="text-sm text-success">Config saved.</p>
      {/if}
      <Button variant="primary" disabled={saving} onclick={save}>Save</Button>
    </div>
  </div>
  <ErrorList name="Save errors" messages={problems} />
  <ConfigFields {fetch} />
</form>
