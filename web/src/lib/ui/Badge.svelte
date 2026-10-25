<script module lang="ts">
  /** The tones of a badge. Each colored tone shows its text on the soft fill of that color. */
  export type BadgeTone = 'neutral' | 'muted' | 'accent' | 'success' | 'warning' | 'danger';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    tone: BadgeTone;
    children: Snippet;
  }

  const { tone, children }: Props = $props();
</script>

<!-- The data attribute picks the tone. A static class string gives Stryker no mutant to survive. -->
<span
  data-tone={tone}
  class="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-px text-xs font-medium whitespace-nowrap data-[tone=accent]:border-accent data-[tone=accent]:bg-accent-soft data-[tone=accent]:text-accent data-[tone=danger]:border-danger data-[tone=danger]:bg-danger-soft data-[tone=danger]:text-danger data-[tone=muted]:border-line data-[tone=muted]:bg-panel data-[tone=muted]:text-muted data-[tone=neutral]:border-line data-[tone=neutral]:bg-panel data-[tone=neutral]:text-ink data-[tone=success]:border-success data-[tone=success]:bg-success-soft data-[tone=success]:text-success data-[tone=warning]:border-warning data-[tone=warning]:bg-warning-soft data-[tone=warning]:text-warning [a:hover>&]:border-current"
>
  {@render children()}
</span>
