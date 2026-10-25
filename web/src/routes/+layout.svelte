<script lang="ts">
  import '../app.css';
  import { onMount, type Snippet } from 'svelte';
  import { page } from '$app/state';
  import { provideRunStream, RunStream } from '#lib/run/stream.ts';
  import Nav from '#lib/shell/Nav.svelte';
  import PhaseBadge from '#lib/shell/PhaseBadge.svelte';
  import ThemeToggle from '#lib/shell/ThemeToggle.svelte';

  const { children }: { children: Snippet } = $props();

  // The context exists before any child runs, so a child can subscribe in its own onMount.
  const stream = new RunStream();
  provideRunStream(stream);

  // A child's onMount runs first, so each subscriber is in place when the stream opens.
  onMount(() => stream.open());
</script>

<svelte:head>
  <title>Jagex account creator</title>
</svelte:head>

<div class="min-h-screen bg-canvas text-ink">
  <header class="border-b border-line bg-surface">
    <div class="mx-auto flex max-w-7xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-2 sm:px-6">
      <p class="font-semibold tracking-tight">Jagex account creator</p>
      <div class="order-last w-full md:order-none md:w-auto">
        <Nav pathname={page.url.pathname} />
      </div>
      <div class="ml-auto flex items-center gap-2">
        <PhaseBadge />
        <ThemeToggle />
      </div>
    </div>
  </header>

  <main class="mx-auto max-w-7xl px-4 py-6 sm:px-6">
    {@render children()}
  </main>
</div>
