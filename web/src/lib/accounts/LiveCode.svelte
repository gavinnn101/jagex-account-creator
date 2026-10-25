<script lang="ts">
  interface Props {
    code: string;
    /** The seconds left in the window of the code, from the server clock. */
    seconds: number;
    /** The page's callback at the end of the window. It runs once, so the page can read the next code. */
    onExpire: () => void;
  }

  const { code, seconds, onExpire }: Props = $props();

  let left = $state(0);

  // Limitation: the countdown runs on the client clock from a value that the server rounds up,
  // so the page can show the old code for the request latency after the window ends.
  $effect(() => {
    left = seconds;
    const tick = setInterval(() => {
      left -= 1;
      if (left <= 0) {
        clearInterval(tick);
        onExpire();
      }
    }, 1000);
    return () => {
      clearInterval(tick);
    };
  });
</script>

<span class="font-mono select-all">{code}</span>
<span class="text-muted">({left} s left)</span>
