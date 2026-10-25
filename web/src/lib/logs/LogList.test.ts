// Component tests load no CSS on their own, and with no height on the region the list renders every row.
import '../../app.css';
import { expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import LogList from './LogList.svelte';

type LogLine = components['schemas']['LogLine'];

const time = '2026-10-21T10:00:00.000+02:00';

function line(msg: string, attrs: Record<string, string> = {}, level = 'INFO'): LogLine {
  return {
    run_id: 1,
    time,
    level,
    msg,
    attrs: Object.entries(attrs).map(([key, value]) => ({ key, value })),
  };
}

// The computed color of a token, in the rgb() form of getComputedStyle.
function tokenColor(token: string): string {
  const hex = getComputedStyle(document.documentElement).getPropertyValue(`--color-${token}`);
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.trim().slice(i, i + 2), 16));
  return `rgb(${String(r)}, ${String(g)}, ${String(b)})`;
}

const many = Array.from({ length: 100_000 }, (_, i) => line(`line ${String(i)}`));

function region(screen: Awaited<ReturnType<typeof render>>): HTMLElement {
  return screen.getByRole('region', { name: 'Log lines' }).element() as HTMLElement;
}

it('renders only the rows in view of 100000 lines', async () => {
  const screen = await render(LogList, { lines: many });
  await expect.element(screen.getByText('line 99999', { exact: true })).toBeVisible();
  const rows = screen.container.querySelectorAll('li');
  expect(rows.length).toBeGreaterThan(0);
  expect(rows.length).toBeLessThan(60);
  expect(screen.getByText('line 0', { exact: true }).elements()).toHaveLength(0);
});

it('renders the first line after a scroll to the top', async () => {
  const screen = await render(LogList, { lines: many });
  await expect.element(screen.getByText('line 99999', { exact: true })).toBeInViewport();
  region(screen).scrollTop = 0;
  await expect.element(screen.getByText('line 0', { exact: true })).toBeInViewport();
});

it('opens at the last line of lines that come all at once, then follows a new line', async () => {
  const screen = await render(LogList, { lines: many });
  await expect
    .element(screen.getByText('line 99999', { exact: true }))
    .toBeInViewport({ ratio: 1 });
  await screen.rerender({ lines: [...many, line('new')] });
  await expect.element(screen.getByText('new', { exact: true })).toBeInViewport({ ratio: 1 });
});

it('shows each part of a line', async () => {
  const screen = await render(LogList, {
    lines: [line('run ended', { run_id: '3', error: 'checking the mailbox: EOF' })],
  });
  const row = screen.getByRole('listitem');
  await expect.element(row).toBeVisible();
  // The exact text, so a lost or a doubled space fails.
  expect(row.element().textContent).toBe(
    '10:00:00.000 INFO run ended run_id=3 error=checking the mailbox: EOF',
  );
  const stamp = row.element().querySelector('time');
  expect(stamp?.getAttribute('datetime')).toBe(time);
  expect(stamp?.getAttribute('title')).toBe(time);
  expect(stamp?.textContent).toBe('10:00:00.000');
});

it('colors the level text by its level', async () => {
  const screen = await render(LogList, {
    lines: ['DEBUG', 'INFO', 'WARN', 'ERROR'].map((level) => line(level.toLowerCase(), {}, level)),
  });
  await expect.element(screen.getByText('error', { exact: true })).toBeVisible();
  const colors = [...screen.container.querySelectorAll('[data-level]')].map((e) => [
    e.textContent,
    getComputedStyle(e).color,
  ]);
  expect(colors).toEqual([
    ['DEBUG', tokenColor('muted')],
    ['INFO', tokenColor('ink')],
    ['WARN', tokenColor('warning')],
    ['ERROR', tokenColor('danger')],
  ]);
});

it('shows a whole byte count in binary units, with the raw count as its title', async () => {
  const screen = await render(LogList, {
    lines: [
      line('account created', {
        browser_bytes_sent: '512',
        browser_bytes_received: '543210',
        http_bytes_sent: '12.5',
        http_bytes_received: 'x12',
        worker: '2048',
      }),
    ],
  });
  const row = screen.getByRole('listitem');
  await expect
    .element(row)
    .toHaveTextContent(
      '10:00:00.000 INFO account created browser_bytes_sent=512B browser_bytes_received=530.5KiB http_bytes_sent=12.5 http_bytes_received=x12 worker=2048',
    );
  const titled = [...row.element().querySelectorAll('span[title]')].map((e) => [
    e.textContent,
    e.getAttribute('title'),
  ]);
  expect(titled).toEqual([
    ['512B', '512'],
    ['530.5KiB', '543210'],
  ]);
});

it('fits the box to a few lines, and caps it at three quarters of the view', async () => {
  const screen = await render(LogList, { lines: [line('a')] });
  await expect.element(screen.getByText('a', { exact: true })).toBeVisible();
  const box = region(screen);
  // One row of 24 px and the two borders.
  expect(box.getBoundingClientRect().height).toBe(26);
  await screen.rerender({ lines: many });
  await expect
    .poll(() => box.getBoundingClientRect().height)
    .toBeCloseTo(window.innerHeight * 0.75, 0);
});

it('expands a record line to its steps', async () => {
  const record = line('account created', {
    email: 'a@example.com',
    'steps.browser_slot_wait': '0s',
    'steps.challenge': '4.161s',
  });
  const screen = await render(LogList, { lines: [line('start'), record] });
  const toggle = screen.getByRole('button', { name: 'Steps' });
  const recordRow = screen.getByRole('listitem').nth(1);
  // The box takes its height from the rows, so the rows show one frame after the first render.
  await expect
    .element(recordRow)
    .toMatchTextContent(/ INFO account created email=a@example\.comSteps$/);
  expect(screen.getByRole('button').elements()).toHaveLength(1);
  await expect.element(toggle).toHaveAttribute('aria-expanded', 'false');
  expect(screen.container.querySelector('dl')).toBeNull();
  const closed = recordRow.element().getBoundingClientRect().height;

  await toggle.click();
  await expect.element(toggle).toHaveAttribute('aria-expanded', 'true');
  const terms = [...screen.container.querySelectorAll('dt, dd')].map((e) => e.textContent);
  expect(terms).toEqual(['browser_slot_wait', '0s', 'challenge', '4.161s']);
  // The next row moves down by the growth of the expanded row.
  const grown = recordRow.element().getBoundingClientRect().height;
  expect(grown).toBeGreaterThan(closed);

  await toggle.click();
  await expect.element(toggle).toHaveAttribute('aria-expanded', 'false');
  expect(screen.container.querySelector('dl')).toBeNull();
});

it('places each row below the one before it, also after a row grows', async () => {
  const record = line('account created', { 'steps.a': '1s', 'steps.b': '2s', 'steps.c': '3s' });
  const screen = await render(LogList, { lines: [record, line('after')] });
  const rows = () => screen.getByRole('listitem').elements();
  const gap = () => {
    const [first, second] = rows().map((r) => r.getBoundingClientRect());
    return (second?.top ?? 0) - (first?.bottom ?? 0);
  };
  await expect.poll(gap).toBe(0);
  await screen.getByRole('button', { name: 'Steps' }).click();
  await expect.poll(gap).toBe(0);
});

it('follows new lines at the end, and shows the lines of a new run', async () => {
  const screen = await render(LogList, { lines: [line('a')] });
  await expect.element(screen.getByText('a', { exact: true })).toBeVisible();
  await screen.rerender({ lines: many });
  await expect.element(screen.getByText('line 99999', { exact: true })).toBeVisible();
  await screen.rerender({ lines: [line('run started')] });
  await expect.element(screen.getByText('run started', { exact: true })).toBeVisible();
  expect(screen.getByRole('listitem').elements()).toHaveLength(1);
});

it('lets the keyboard focus the region', async () => {
  const screen = await render(LogList, { lines: [line('a')] });
  const scroller = region(screen);
  scroller.focus();
  expect(document.activeElement).toBe(scroller);
});
