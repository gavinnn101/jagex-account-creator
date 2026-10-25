<script lang="ts">
  import Copy from '@lucide/svelte/icons/copy';
  import Eye from '@lucide/svelte/icons/eye';
  import EyeOff from '@lucide/svelte/icons/eye-off';
  import Button from '#lib/ui/Button.svelte';

  interface Props {
    /** The name of the secret in the button names, for example "password". */
    label: string;
    value: string;
  }

  const { label, value }: Props = $props();

  let shown = $state(false);
  let note = $state('');
  // Only a secure context has a clipboard, so a LAN listen address over http has none.
  const canCopy = 'clipboard' in navigator;
  const toggleName = $derived(`${shown ? 'Hide' : 'Reveal'} ${label}`);

  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(value);
      note = 'Copied';
    } catch {
      note = 'Copy failed';
    }
  }
</script>

<!-- The row never wraps: a long value breaks inside its own box, and the buttons stay beside it. -->
<div class="flex items-center gap-1">
  {#if shown}
    <span class="min-w-0 font-mono break-all select-all">{value}</span>
  {:else}
    <span class="text-muted">Hidden</span>
  {/if}
  <Button
    variant="ghost"
    size="icon"
    aria-label={toggleName}
    title={toggleName}
    onclick={() => {
      shown = !shown;
    }}
  >
    {#if shown}
      <EyeOff aria-hidden="true" class="size-4" />
    {:else}
      <Eye aria-hidden="true" class="size-4" />
    {/if}
  </Button>
  {#if canCopy}
    <Button
      variant="ghost"
      size="icon"
      aria-label="Copy {label}"
      title="Copy {label}"
      onclick={copy}><Copy aria-hidden="true" class="size-4" /></Button
    >
    <span role="status" class="text-sm whitespace-nowrap text-muted">{note}</span>
  {/if}
</div>
