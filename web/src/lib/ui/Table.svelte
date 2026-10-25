<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    /** The caption of the table, and the name of its scroll region. Unique on each screen. */
    caption: string;
    /** The header cell of each column. */
    columns: string[];
    /** Pins the last column to the right edge of the region, so the actions of a wide row stay in view. */
    stickyActions?: boolean;
    /** The rows of the body: tr elements with one td for each column. */
    children: Snippet;
  }

  const { caption, columns, stickyActions = false, children }: Props = $props();
</script>

<!-- A wide table scrolls in its own region, so the keyboard must be able to focus it. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
  role="region"
  aria-label={caption}
  tabindex="0"
  class="max-w-full overflow-x-auto rounded-md border border-line bg-surface"
>
  <!-- The data attribute pins the last column. A pinned cell is opaque, so the scrolled cells do not show through it. -->
  <!-- Chrome paints no collapsed border on a sticky cell, so an inset shadow draws the separator. -->
  <table
    data-sticky-actions={stickyActions}
    class="w-full border-collapse text-left text-sm tabular-nums [&_td]:border-t [&_td]:border-line [&_td]:px-3 [&_td]:py-1.5 [&_td]:align-baseline data-[sticky-actions=true]:[&_td:last-child]:bg-surface [&_th]:px-3 [&_th]:py-1.5 [&_th]:font-semibold [&_th]:whitespace-nowrap [&_th]:text-muted data-[sticky-actions=true]:[&_th:last-child]:bg-panel data-[sticky-actions=true]:[&_tr>:last-child]:sticky data-[sticky-actions=true]:[&_tr>:last-child]:right-0 data-[sticky-actions=true]:[&_tr>:last-child]:shadow-[inset_1px_0_0_var(--color-line)]"
  >
    <caption class="border-b border-line px-3 py-2 text-left font-semibold">{caption}</caption>
    <thead class="bg-panel">
      <tr>
        {#each columns as column (column)}
          <th scope="col">{column}</th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {@render children()}
    </tbody>
  </table>
</div>
