<script lang="ts">
  import Moon from '@lucide/svelte/icons/moon';
  import Sun from '@lucide/svelte/icons/sun';
  import Button from '#lib/ui/Button.svelte';
  import { applyTheme, currentTheme } from './theme.ts';

  // The attribute, not storage, gives the start value, so it shows what the head script set.
  let theme = $state(currentTheme());

  function toggle(): void {
    theme = theme === 'light' ? 'dark' : 'light';
    applyTheme(theme);
  }
</script>

<Button
  variant="ghost"
  size="icon"
  aria-pressed={theme === 'light'}
  title="Light theme"
  onclick={toggle}
>
  <span class="sr-only">Light theme</span>
  {#if theme === 'light'}
    <Sun aria-hidden="true" class="size-4" />
  {:else}
    <Moon aria-hidden="true" class="size-4" />
  {/if}
</Button>
