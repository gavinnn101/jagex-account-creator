<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';

  interface Props extends HTMLButtonAttributes {
    /** The rank of the action. Each screen region has at most one primary action. */
    variant: 'primary' | 'secondary' | 'ghost' | 'danger';
    /** An icon button is square. It needs a text name: an aria-label or an sr-only span. */
    size?: 'default' | 'icon';
    children: Snippet;
  }

  const { variant, size = 'default', children, ...rest }: Props = $props();
</script>

<!-- The data attributes pick the style. A static class string gives Stryker no mutant to survive. -->
<button
  type="button"
  data-variant={variant}
  data-size={size}
  class="inline-flex shrink-0 items-center justify-center gap-1.5 rounded-md border text-sm font-semibold whitespace-nowrap transition-colors disabled:cursor-not-allowed disabled:border-line disabled:bg-panel disabled:text-muted data-[size=default]:h-8 data-[size=default]:px-3 data-[size=icon]:size-8 data-[variant=danger]:enabled:border-danger data-[variant=danger]:enabled:bg-danger data-[variant=danger]:enabled:text-on-danger data-[variant=danger]:enabled:hover:border-danger-strong data-[variant=danger]:enabled:hover:bg-danger-strong data-[variant=ghost]:enabled:border-transparent data-[variant=ghost]:enabled:text-accent data-[variant=ghost]:enabled:hover:bg-accent-soft data-[variant=primary]:enabled:border-accent data-[variant=primary]:enabled:bg-accent data-[variant=primary]:enabled:text-on-accent data-[variant=primary]:enabled:hover:border-accent-strong data-[variant=primary]:enabled:hover:bg-accent-strong data-[variant=secondary]:enabled:border-line-strong data-[variant=secondary]:enabled:bg-surface data-[variant=secondary]:enabled:text-ink data-[variant=secondary]:enabled:hover:bg-panel"
  {...rest}
>
  {@render children()}
</button>
