import { afterEach, expect, it, vi } from 'vitest';
import type { Locator } from 'vitest/browser';
import { render } from 'vitest-browser-svelte';
import '../../app.css';
import type { components } from '#lib/api.gen.d.ts';
import { RunStream, runStreamKey } from '#lib/run/stream.ts';
import {
  type FakeEventSource,
  lastSource,
  runState,
  stubEventSource,
} from '#lib/streamtest/streamtest.ts';
import PhaseBadge from './PhaseBadge.svelte';

type Phase = components['schemas']['RunState']['phase'];

afterEach(() => {
  vi.unstubAllGlobals();
});

// Renders the badge under a stream that got the events of before. It records each text and tone that the render replaced.
async function renderBadge(before: (source: FakeEventSource) => void = () => undefined) {
  stubEventSource();
  const stream = new RunStream();
  stream.open();
  const source = lastSource();
  before(source);
  const replaced: string[] = [];
  const observer = new MutationObserver((records) => {
    for (const r of records) if (r.oldValue !== null) replaced.push(r.oldValue);
  });
  observer.observe(document.body, {
    subtree: true,
    attributeFilter: ['data-tone'],
    attributeOldValue: true,
    characterDataOldValue: true,
  });
  const screen = await render(PhaseBadge, { context: new Map([[runStreamKey, stream]]) });
  observer.takeRecords().forEach((r) => {
    if (r.oldValue !== null) replaced.push(r.oldValue);
  });
  observer.disconnect();
  const link = screen.getByRole('link');
  await expect.element(link).toBeVisible();
  return { link, source, replaced };
}

// The pill inside the link.
function pill(link: Locator): HTMLElement {
  const badge = link.element().firstElementChild;
  if (!(badge instanceof HTMLElement)) throw new Error('no badge');
  return badge;
}

it.for<[Phase | undefined, string, string, string]>([
  [undefined, 'Connecting', 'connecting', 'muted'],
  ['idle', 'Idle', 'idle', 'muted'],
  ['running', 'Running', 'running', 'success'],
  ['stopping', 'Stopping, stage 1 of 2', 'stopping', 'warning'],
  ['stopping_now', 'Stopping now, stage 2 of 2', 'stopping_now', 'warning'],
  ['ended', 'Ended', 'ended', 'neutral'],
])('names the phase %s and links the run screen', async ([phase, text, tone, badgeTone]) => {
  const { link } = await renderBadge((s) => {
    if (phase !== undefined) s.emit(runState(phase));
  });
  await expect.element(link).toHaveAccessibleName(`Run phase: ${text}`);
  await expect.element(link).toHaveAttribute('href', '/');
  await expect.element(link).toHaveAttribute('data-tone', tone);
  expect(pill(link).dataset.tone).toBe(badgeTone);
});

it('pulses the dot only while the run runs', async () => {
  const { link, source } = await renderBadge((s) => {
    s.emit(runState('running'));
  });
  await expect.element(link).toHaveAttribute('data-tone', 'running');
  const dot = pill(link).querySelector('[aria-hidden="true"]');
  if (dot === null) throw new Error('no dot');
  expect(getComputedStyle(dot).animationName).toBe('pulse');
  expect(getComputedStyle(dot).backgroundColor).toBe(getComputedStyle(pill(link)).color);
  source.emit(runState('ended'));
  await expect.element(link).toHaveAttribute('data-tone', 'ended');
  expect(getComputedStyle(dot).animationName).toBe('none');
});

it('draws the border in the text color on hover', async () => {
  const { link } = await renderBadge((s) => {
    s.emit(runState('idle'));
  });
  const before = getComputedStyle(pill(link)).borderColor;
  expect(before).not.toBe(getComputedStyle(pill(link)).color);
  await link.hover();
  await expect
    .poll(() => getComputedStyle(pill(link)).borderColor)
    .toBe(getComputedStyle(pill(link)).color);
});

it('never shows a reconnect before a stream error, not even in the first render', async () => {
  const { replaced } = await renderBadge();
  expect(replaced.filter((v) => /reconnecting/i.test(v))).toEqual([]);
});

it('hides the phase while the stream reconnects', async () => {
  const { link, source } = await renderBadge((s) => {
    s.emit(runState('running'));
  });
  await expect.element(link).toHaveAttribute('data-tone', 'running');
  source.fail();
  await expect.element(link).toHaveAccessibleName('Run phase: Reconnecting');
  await expect.element(link).toHaveAttribute('data-tone', 'reconnecting');
  expect(pill(link).dataset.tone).toBe('danger');
  source.emit(runState('ended'));
  await expect.element(link).toHaveAccessibleName('Run phase: Ended');
});

it('shows a reconnect that the stream holds at once', async () => {
  const { link } = await renderBadge((s) => {
    s.fail();
  });
  await expect.element(link).toHaveAccessibleName('Run phase: Reconnecting');
});
