<script lang="ts">
  import { apiClient } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import Button from '#lib/ui/Button.svelte';

  type EmailConfig = components['schemas']['EmailConfig'];
  type Result = components['schemas']['MailboxCheck'];

  interface Props {
    /** The edited email section. The check logs in with it, so a save is not necessary first. */
    email: EmailConfig;
    fetch: typeof globalThis.fetch;
    /** Turns the button off, for an email section that the check cannot log in with. */
    disabled?: boolean;
  }

  const { email, fetch, disabled = false }: Props = $props();

  let checking = $state(false);
  let result: Result | undefined = $state();

  async function check(): Promise<void> {
    checking = true;
    result = undefined;
    try {
      const { data, error } = await apiClient(fetch).POST('/api/checks/mailbox', { body: email });
      result = error === undefined ? data : { ok: false, error: error.message };
    } catch (e) {
      result = { ok: false, error: String(e) };
    }
    checking = false;
  }
</script>

<div class="flex flex-wrap items-center gap-gap">
  <Button variant="secondary" disabled={disabled || checking} onclick={check}>Check mailbox</Button>
  {#if result !== undefined}
    {#if result.ok}
      <p role="status">Login OK.</p>
    {:else}
      <p role="alert" class="text-danger">Mailbox check failed: {result.error}</p>
    {/if}
  {/if}
</div>
