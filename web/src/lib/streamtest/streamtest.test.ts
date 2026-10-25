import { afterEach, expect, it, vi } from 'vitest';
import { FakeEventSource, lastSource, runState, stubEventSource } from './streamtest.ts';

afterEach(() => {
  vi.unstubAllGlobals();
});

it('stubs the global EventSource and clears the count and the last source', () => {
  new FakeEventSource('/before');
  stubEventSource();
  expect(FakeEventSource.count).toBe(0);
  expect(FakeEventSource.last).toBeUndefined();
  const source = new EventSource('/api/events');
  expect(source).toBeInstanceOf(FakeEventSource);
  expect(FakeEventSource.count).toBe(1);
  expect(lastSource()).toBe(source);
});

it('throws when the code made no source', () => {
  stubEventSource();
  expect(() => lastSource()).toThrow(new Error('no EventSource'));
});

it('keeps the url and starts open with no handler', () => {
  stubEventSource();
  const source = new FakeEventSource('/api/logs');
  new FakeEventSource('/api/events');
  expect(FakeEventSource.count).toBe(2);
  expect(source.url).toBe('/api/logs');
  expect(source.onmessage).toBeNull();
  expect(source.onerror).toBeNull();
  expect(source.closed).toBe(false);
  source.close();
  expect(source.closed).toBe(true);
});

it('ignores a message and an error with no handler', () => {
  const source = new FakeEventSource('/api/events');
  expect(() => {
    source.send('x');
    source.fail();
  }).not.toThrow();
});

it('sends text and JSON as message events, and an error to its handler', () => {
  const source = new FakeEventSource('/api/events');
  const got: string[] = [];
  source.onmessage = (e) => got.push(`${e.type} ${e.data}`);
  source.onerror = () => got.push('error');
  source.send('raw text');
  source.emit({ phase: 'idle' });
  source.fail();
  expect(got).toEqual(['message raw text', 'message {"phase":"idle"}', 'error']);
});

it('builds a run state with zero counts, and over replaces its fields', () => {
  const counts = {
    attempts: 0,
    accounts: 0,
    unconfirmed: 0,
    failures: {},
    browser_bytes_sent: 0,
    browser_bytes_received: 0,
    http_bytes_sent: 0,
    http_bytes_received: 0,
  };
  expect(runState('idle')).toStrictEqual({ phase: 'idle', workers: [], cooldowns: [], counts });
  expect(runState('ended', { run_id: 4, cooldowns: [] })).toStrictEqual({
    phase: 'ended',
    run_id: 4,
    workers: [],
    cooldowns: [],
    counts,
  });
});
