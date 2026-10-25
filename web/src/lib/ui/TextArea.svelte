<script lang="ts">
  import type { HTMLTextareaAttributes } from 'svelte/elements';

  interface Props extends Omit<HTMLTextareaAttributes, 'value'> {
    label: string;
    /** A line of help text below the text area. */
    hint?: string;
    value?: string;
  }

  let { label, hint, value = $bindable(), ...rest }: Props = $props();
  const id = $props.id();
</script>

<div class="flex flex-col gap-1">
  <label for={id} class="text-sm font-semibold">{label}</label>
  <textarea
    {id}
    class="w-full min-w-0 rounded-md border border-line-strong bg-surface px-2 py-1 font-mono text-sm"
    aria-describedby={hint === undefined ? undefined : `${id}-hint`}
    {...rest}
    bind:value></textarea>
  {#if hint !== undefined}
    <p id="{id}-hint" class="text-sm text-muted">{hint}</p>
  {/if}
</div>
