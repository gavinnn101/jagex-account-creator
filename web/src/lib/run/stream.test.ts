import { afterEach, expect, it, vi } from 'vitest';
import {
  FakeEventSource,
  lastSource,
  runState,
  stubEventSource,
} from '#lib/streamtest/streamtest.ts';
import { RunStream } from './stream.ts';

afterEach(() => {
  vi.unstubAllGlobals();
});

function open() {
  stubEventSource();
  const stream = new RunStream();
  const close = stream.open();
  return { stream, close, source: lastSource() };
}

// Records each call of the handlers of one subscriber, in order.
function record(stream: RunStream) {
  const calls: unknown[] = [];
  const unsubscribe = stream.subscribe({
    onMessage: (v) => calls.push(v.phase),
    onReconnecting: (r) => calls.push(r),
  });
  return { calls, unsubscribe };
}

it('opens one stream of the run state and closes it', () => {
  const { stream, close, source } = open();
  record(stream);
  record(stream);
  expect(FakeEventSource.count).toBe(1);
  expect(source.url).toBe('/api/events');
  expect(source.closed).toBe(false);
  close();
  expect(source.closed).toBe(true);
});

it('gives each event to each subscriber', () => {
  const { stream, source } = open();
  const a = record(stream);
  const b = record(stream);
  source.emit(runState('running'));
  source.fail();
  const want = [false, false, 'running', true];
  expect(a.calls).toEqual(want);
  expect(b.calls).toEqual(want);
});

it('replays the flag and no state to a subscriber before the first event', () => {
  const { stream } = open();
  expect(record(stream).calls).toEqual([false]);
});

it('replays the flag and the last state to a new subscriber', () => {
  const { stream, source } = open();
  source.emit(runState('idle'));
  source.emit(runState('running'));
  expect(record(stream).calls).toEqual([false, 'running']);
});

it('replays a reconnect with the stale state to a new subscriber', () => {
  const { stream, source } = open();
  source.emit(runState('running'));
  source.fail();
  expect(record(stream).calls).toEqual([true, 'running']);
  source.emit(runState('ended'));
  expect(record(stream).calls).toEqual([false, 'ended']);
});

it('gives nothing to a subscriber that left', () => {
  const { stream, source } = open();
  const gone = record(stream);
  const kept = record(stream);
  gone.unsubscribe();
  source.emit(runState('running'));
  source.fail();
  expect(gone.calls).toEqual([false]);
  expect(kept.calls).toEqual([false, false, 'running', true]);
});
