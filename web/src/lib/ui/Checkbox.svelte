<script lang="ts">
  interface Props {
    label: string;
    /** A line of help text below the checkbox. */
    hint?: string;
    checked?: boolean;
  }

  let { label, hint, checked = $bindable() }: Props = $props();
  const id = $props.id();
</script>

<div class="flex flex-col gap-1">
  <label class="flex items-center gap-2">
    <input
      type="checkbox"
      class="size-4 accent-accent"
      aria-describedby={hint === undefined ? undefined : `${id}-hint`}
      bind:checked
    />
    {label}
  </label>
  {#if hint !== undefined}
    <p id="{id}-hint" class="text-sm text-muted">{hint}</p>
  {/if}
</div>
