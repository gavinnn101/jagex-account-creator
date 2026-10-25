<script lang="ts">
  import { onMount } from 'svelte';
  import { resolve } from '$app/paths';
  import type { components } from '#lib/api.gen.d.ts';
  import { phaseText } from '#lib/run/stats.ts';
  import { runStream } from '#lib/run/stream.ts';
  import Badge, { type BadgeTone } from '#lib/ui/Badge.svelte';

  type Phase = components['schemas']['RunState']['phase'];
  type Tone = Phase | 'connecting' | 'reconnecting';

  const badgeTones: Record<Tone, BadgeTone> = {
    connecting: 'muted',
    idle: 'muted',
    running: 'success',
    stopping: 'warning',
    stopping_now: 'warning',
    ended: 'neutral',
    reconnecting: 'danger',
  };

  const stream = runStream();
  const home = resolve('/'); // mutation-ignore: StringLiteral: '' also resolves to /

  let phase: Phase | undefined = $state();
  let reconnecting = $state(false);

  // A stale phase can mislead, so a reconnect hides it.
  const tone: Tone = $derived(reconnecting ? 'reconnecting' : (phase ?? 'connecting'));
  const text = $derived.by(() => {
    if (reconnecting) {
      return 'Reconnecting';
    }
    return phase === undefined ? 'Connecting' : phaseText[phase];
  });

  onMount(() =>
    stream.subscribe({
      onMessage: (state) => {
        phase = state.phase;
      },
      onReconnecting: (value) => {
        reconnecting = value;
      },
    }),
  );
</script>

<a href={home} data-tone={tone} class="group inline-flex rounded-full">
  <Badge tone={badgeTones[tone]}>
    <span
      aria-hidden="true"
      class="size-2 rounded-full bg-current group-data-[tone=running]:animate-pulse motion-reduce:animate-none"
    ></span>
    <span><span class="sr-only">Run phase:</span> {text}</span>
  </Badge>
</a>
