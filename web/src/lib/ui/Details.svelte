<script lang="ts">
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import { untrack, type Snippet } from 'svelte';

  interface Props {
    /** The name of the section. It is the text of the summary. */
    summary: string;
    /** One line of the values of the section. It shows only while the section is closed. */
    digest?: string;
    /** The start state. The browser owns the state after that. */
    open?: boolean;
    children: Snippet;
  }

  const { summary, digest, open = false, children }: Props = $props();
  // The section keeps its own state, so a later change of the prop or a redraw never undoes a click.
  let shown = $state(untrack(() => open));
</script>

<details bind:open={shown} class="group rounded-md border border-line bg-surface">
  <summary
    class="flex cursor-pointer list-none flex-wrap items-baseline gap-x-3 gap-y-0.5 rounded-md px-3 py-2 hover:bg-panel [&::-webkit-details-marker]:hidden"
  >
    <span class="inline-flex items-center gap-1.5 font-semibold">
      <ChevronRight
        aria-hidden="true"
        class="size-4 shrink-0 text-muted transition-transform group-open:rotate-90 motion-reduce:transition-none"
      />
      {summary}
    </span>
    {#if digest !== undefined}
      <span class="min-w-0 text-sm break-words text-muted group-open:hidden">{digest}</span>
    {/if}
  </summary>
  <div class="border-t border-line px-3 py-3">
    {@render children()}
  </div>
</details>
