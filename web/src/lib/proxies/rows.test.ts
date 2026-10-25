import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { runState } from '#lib/streamtest/streamtest.ts';
import {
  cooldownText,
  cooldownsByProxy,
  proxyColumns,
  runEnded,
  testView,
  totalBytes,
  type TestState,
} from './rows.ts';

type Proxy = components['schemas']['Proxy'];
type RunState = components['schemas']['RunState'];
type RunCooldown = components['schemas']['RunCooldown'];

const a: RunCooldown = {
  proxy: 'a.example:1',
  reason: 'rate limited',
  until: '2026-10-22T10:05:00Z',
  failures: 2,
};
const b: RunCooldown = {
  proxy: 'socks5://b.example:2',
  reason: 'check failed',
  until: '2026-10-22T10:01:00Z',
  failures: 1,
};

function state(phase: RunState['phase'], cooldowns: RunCooldown[]): RunState {
  return runState(phase, { cooldowns });
}

function proxy(id: number, over: Partial<Proxy> = {}): Proxy {
  return {
    id,
    proxy: `p${String(id)}.example:8080`,
    auth: false,
    enabled: true,
    added_at: '2026-10-22T10:00:00Z',
    attempts: 0,
    successes: 0,
    browser_bytes_sent: 0,
    browser_bytes_received: 0,
    http_bytes_sent: 0,
    http_bytes_received: 0,
    ...over,
  };
}

describe('testView', () => {
  it('marks a test in flight', () => {
    expect(testView({ kind: 'running' })).toEqual({ tone: 'muted', label: 'Testing', detail: '' });
  });

  it('gives the time of a passed test', () => {
    expect(testView({ kind: 'answered', result: { ok: true, duration_ms: 12 } })).toEqual({
      tone: 'success',
      label: 'OK in 12 ms',
      detail: '',
    });
  });

  it('gives the time and the error of a failed test', () => {
    const result = { ok: false, error: 'dial tcp 127.0.0.1:9: refused', duration_ms: 3 };
    expect(testView({ kind: 'answered', result })).toEqual({
      tone: 'danger',
      label: 'Failed in 3 ms',
      detail: 'dial tcp 127.0.0.1:9: refused',
    });
    expect(testView({ kind: 'answered', result: { ok: false, duration_ms: 0 } })).toEqual({
      tone: 'danger',
      label: 'Failed in 0 ms',
      detail: '',
    });
  });

  it('gives the message of a failed request', () => {
    expect(testView({ kind: 'failed', message: 'proxy 9 not found' })).toEqual({
      tone: 'danger',
      label: 'Request failed',
      detail: 'proxy 9 not found',
    });
  });
});

describe('proxyColumns', () => {
  const none = {
    health: false,
    lastAttempt: false,
    lastFailure: false,
    cooldown: false,
    test: false,
  };

  it('shows no optional column for proxies with no value', () => {
    expect(proxyColumns([proxy(1), proxy(2)], new Map(), new Map())).toEqual(none);
    expect(proxyColumns([], new Map(), new Map())).toEqual(none);
  });

  it('shows the health when some proxy has an attempt', () => {
    const used = proxy(2, { attempts: 1, last_attempt_at: '2026-10-22T11:00:00Z' });
    expect(proxyColumns([proxy(1), used], new Map(), new Map())).toEqual({
      ...none,
      health: true,
      lastAttempt: true,
    });
  });

  it('reads each field on its own', () => {
    expect(proxyColumns([proxy(1, { attempts: 1 })], new Map(), new Map())).toEqual({
      ...none,
      health: true,
    });
    const at = proxy(1, { last_attempt_at: '2026-10-22T11:00:00Z' });
    expect(proxyColumns([at], new Map(), new Map())).toEqual({ ...none, lastAttempt: true });
    const failed = proxy(1, { last_failure: 'challenge' });
    expect(proxyColumns([proxy(2), failed], new Map(), new Map())).toEqual({
      ...none,
      lastFailure: true,
    });
  });

  it('shows the cooldown when a listed proxy has one', () => {
    const listed = proxy(1, { proxy: a.proxy });
    expect(proxyColumns([proxy(2), listed], new Map([[a.proxy, a]]), new Map())).toEqual({
      ...none,
      cooldown: true,
    });
    expect(proxyColumns([proxy(2)], new Map([[a.proxy, a]]), new Map())).toEqual(none);
  });

  it('shows the test when a listed proxy has a test state', () => {
    const tests = new Map<number, TestState>([[2, { kind: 'running' }]]);
    expect(proxyColumns([proxy(1), proxy(2)], new Map(), tests)).toEqual({ ...none, test: true });
  });

  it('ignores the test entry of a proxy that is no longer listed', () => {
    const tests = new Map<number, TestState>([[9, { kind: 'running' }]]);
    expect(proxyColumns([proxy(1), proxy(2)], new Map(), tests)).toEqual(none);
  });
});

describe('totalBytes', () => {
  it('adds the four byte counts', () => {
    const p = proxy(1, {
      browser_bytes_sent: 1,
      browser_bytes_received: 20,
      http_bytes_sent: 300,
      http_bytes_received: 4000,
    });
    expect(totalBytes(p)).toBe(4321);
  });
});

describe('cooldownsByProxy', () => {
  it('maps each cooldown to its proxy while a run is in progress', () => {
    for (const phase of ['running', 'stopping', 'stopping_now'] as const) {
      const map = cooldownsByProxy(state(phase, [a, b]));
      expect([...map]).toEqual([
        [a.proxy, a],
        [b.proxy, b],
      ]);
    }
  });

  it('holds none with no run in progress', () => {
    expect(cooldownsByProxy(undefined).size).toBe(0);
    expect(cooldownsByProxy(state('idle', [a])).size).toBe(0);
    expect(cooldownsByProxy(state('ended', [a])).size).toBe(0);
  });
});

describe('cooldownText', () => {
  it('names the reason and the time to the end, as the run screen does', () => {
    const now = new Date('2026-10-22T10:03:30Z');
    expect(cooldownText(a, now)).toBe('rate limited, ends in 1:30');
    expect(cooldownText(b, now)).toBe('check failed, ends in 0:00');
  });
});

describe('runEnded', () => {
  it('is true only on a change to ended', () => {
    expect(runEnded('running', 'ended')).toBe(true);
    expect(runEnded('stopping', 'ended')).toBe(true);
    expect(runEnded(undefined, 'ended')).toBe(false);
    expect(runEnded('ended', 'ended')).toBe(false);
    expect(runEnded('ended', 'running')).toBe(false);
    expect(runEnded(undefined, 'running')).toBe(false);
  });
});
