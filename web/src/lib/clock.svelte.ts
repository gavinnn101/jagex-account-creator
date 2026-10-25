import { onMount } from 'svelte';

// A tick replaces the Date and never changes it in place. A SvelteDate fails under the fake Date of a test.
class Clock {
  #now = $state(new Date());

  /** The time of the last tick. */
  get now(): Date {
    return this.#now;
  }

  tick(): void {
    this.#now = new Date();
  }
}

/**
 * Returns a clock whose now is the time of its last tick, one tick each periodMs.
 * Call it at component init, because onMount ties the interval to that component
 * and clears it on unmount. A page makes one clock, so it has one timer for all its times.
 */
export function clock(periodMs: number): { readonly now: Date } {
  const time = new Clock();
  onMount(() => {
    const interval = setInterval(() => {
      time.tick();
    }, periodMs);
    return () => {
      clearInterval(interval);
    };
  });
  return time;
}
