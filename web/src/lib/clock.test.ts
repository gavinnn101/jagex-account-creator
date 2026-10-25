import { afterEach, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import ClockHost from '#lib/clocktest/ClockHost.svelte';

afterEach(() => {
  vi.useRealTimers();
});

const start = Date.parse('2026-10-21T10:00:00.000Z');

it.for([1000, 30_000])('ticks every %d ms and stops on unmount', async (periodMs) => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'], now: start });
  const screen = await render(ClockHost, { periodMs });
  const shown = () => screen.container.querySelector('p')?.textContent;
  expect(shown()).toBe('2026-10-21T10:00:00.000Z');
  expect(vi.getTimerCount()).toBe(1);
  vi.advanceTimersByTime(periodMs - 1);
  // setTimeout is real here, so this lets any redraw run.
  await new Promise((done) => setTimeout(done, 0));
  expect(shown()).toBe('2026-10-21T10:00:00.000Z');
  vi.advanceTimersByTime(1);
  await expect.poll(shown).toBe(new Date(start + periodMs).toISOString());
  await screen.unmount();
  expect(vi.getTimerCount()).toBe(0);
});
