<script lang="ts">
  // The host of the clock test, because clock() must run at component init. Only test files import it.
  import { untrack } from 'svelte';
  import { clock } from '#lib/clock.svelte.ts';

  const { periodMs }: { periodMs: number } = $props();
  // A clock keeps the period that it starts with.
  const time = untrack(() => clock(periodMs));
</script>

<p>{time.now.toISOString()}</p>
