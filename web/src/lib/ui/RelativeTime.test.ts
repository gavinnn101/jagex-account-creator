import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import RelativeTime from './RelativeTime.svelte';

it('shows the relative time with the machine time and the full time', async () => {
  const screen = await render(RelativeTime, {
    iso: '2026-10-20T09:03:11.1234567Z',
    now: new Date('2026-10-20T09:08:12Z'),
  });
  const time = screen.container.querySelector('time');
  expect(time?.textContent).toBe('5m ago');
  expect(time?.getAttribute('datetime')).toBe('2026-10-20T09:03:11.123Z');
  expect(time?.getAttribute('title')).toBe('Oct 20, 2026, 2:33:11 PM GMT+5:30');
  expect(getComputedStyle(time as Element).whiteSpace).toBe('nowrap');
});

it('shows the time of the clock that the page passes', async () => {
  const screen = await render(RelativeTime, {
    iso: '2026-10-20T09:03:11Z',
    now: new Date('2026-10-20T09:03:11Z'),
  });
  const time = () => screen.container.querySelector('time')?.textContent;
  expect(time()).toBe('now');
  await screen.rerender({ now: new Date('2026-10-20T12:03:11Z') });
  await expect.poll(time).toBe('3h ago');
});
