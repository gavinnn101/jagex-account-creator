<script lang="ts" generics="T extends string | number">
  import type { HTMLInputAttributes } from 'svelte/elements';

  interface Props extends Omit<HTMLInputAttributes, 'value'> {
    label: string;
    /** A line of help text below the input. */
    hint?: string;
    /** The input value. An empty number input gives null. */
    value?: T | null;
  }

  let { label, hint, value = $bindable(), ...rest }: Props = $props();
  const id = $props.id();
</script>

<!-- The attribute, not :read-only, marks a read-only input. A file input matches :read-only. -->
<!-- h-8 is the height of a Button, so an input and a button line up in a row. -->
<div class="flex flex-col gap-1">
  <label for={id} class="text-sm font-semibold">{label}</label>
  <input
    {id}
    class="h-8 w-full min-w-0 rounded-md border border-line-strong bg-surface px-2 file:mr-3 file:rounded file:border file:border-line-strong file:bg-panel file:px-2 file:text-sm file:font-semibold [&[readonly]]:bg-panel [&[readonly]]:text-muted"
    aria-describedby={hint === undefined ? undefined : `${id}-hint`}
    {...rest}
    bind:value
  />
  {#if hint !== undefined}
    <p id="{id}-hint" class="text-sm text-muted">{hint}</p>
  {/if}
</div>
