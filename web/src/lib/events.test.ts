import { afterEach, expect, it, vi } from 'vitest';
import { subscribe } from './events.ts';
import { lastSource, stubEventSource } from '#lib/streamtest/streamtest.ts';

afterEach(() => {
  vi.unstubAllGlobals();
});

function open() {
  stubEventSource();
  const calls: unknown[] = [];
  const close = subscribe<{ phase: string }>('/api/events', {
    onMessage: (v) => calls.push(v),
    onReconnecting: (r) => calls.push(r),
  });
  return { calls, close, source: lastSource() };
}

it('decodes each message and closes the stream', () => {
  const { calls, close, source } = open();
  expect(source.url).toBe('/api/events');
  source.send('{"phase":"running"}');
  expect(calls).toEqual([false, { phase: 'running' }]);
  expect(source.closed).toBe(false);
  close();
  expect(source.closed).toBe(true);
});

it('reports reconnecting from an error until the next message', () => {
  const { calls, source } = open();
  source.fail();
  expect(calls).toEqual([true]);
  source.send('{"phase":"ended"}');
  expect(calls).toEqual([true, false, { phase: 'ended' }]);
});
