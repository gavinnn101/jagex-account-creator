import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { runState } from '#lib/streamtest/streamtest.ts';
import {
  accountTarget,
  accountsPerHour,
  byteTotals,
  bytesPerAccount,
  failedOfAttempts,
  failuresByCategory,
  runTime,
  successRate,
} from './stats.ts';

type RunState = components['schemas']['RunState'];
type RunCounts = components['schemas']['RunCounts'];

function counts(over: Partial<RunCounts> = {}): RunCounts {
  return { ...runState('running').counts, ...over };
}

function state(over: Partial<RunState> = {}, c: Partial<RunCounts> = {}): RunState {
  return runState('running', { counts: counts(c), ...over });
}

describe('accountTarget', () => {
  it('names the target of the run and gives it as the max', () => {
    expect(accountTarget(state({ account_count: 10 }))).toEqual({ text: 'of 10', max: 10 });
  });
  it('gives no max to a run with no limit', () => {
    expect(accountTarget(state({ account_count: -1 }))).toEqual({ text: 'No limit' });
  });
  it('is undefined before the first run', () => {
    expect(accountTarget(state({ phase: 'idle' }))).toBeUndefined();
  });
});

describe('runTime', () => {
  const now = new Date('2026-10-21T10:00:00.000Z');
  it('counts to now while the run runs', () => {
    expect(runTime(state({ started_at: '2026-10-21T09:29:59.500Z' }), now)).toBe('30:00');
  });
  it('counts to the end of an ended run', () => {
    const s = state({
      phase: 'ended',
      started_at: '2026-10-21T08:00:00.000Z',
      ended_at: '2026-10-21T09:02:05.000Z',
    });
    expect(runTime(s, now)).toBe('1:02:05');
  });
  it('gives 0:00 for a start after now', () => {
    expect(runTime(state({ started_at: '2026-10-21T10:00:05.000Z' }), now)).toBe('0:00');
  });
  it('is a dash before the first run', () => {
    expect(runTime(state({ phase: 'idle' }), now)).toBe('—');
  });
});

describe('accountsPerHour', () => {
  const now = new Date('2026-10-21T10:00:00.000Z');
  it('counts to now while the run runs', () => {
    const s = state({ started_at: '2026-10-21T09:45:00.000Z' }, { accounts: 3 });
    expect(accountsPerHour(s, now)).toBe('12.0');
  });
  it('counts to the end of an ended run', () => {
    const s = state(
      {
        phase: 'ended',
        started_at: '2026-10-21T09:00:00.000Z',
        ended_at: '2026-10-21T09:40:00.000Z',
      },
      { accounts: 7 },
    );
    expect(accountsPerHour(s, now)).toBe('10.5');
  });
  it.each([
    ['before the first run', state({ phase: 'idle' })],
    ['at a run time of 0', state({ started_at: '2026-10-21T10:00:00.000Z' }, { accounts: 1 })],
    ['for a start after now', state({ started_at: '2026-10-21T10:00:05.000Z' }, { accounts: 1 })],
  ])('is a dash %s', (_, s) => {
    expect(accountsPerHour(s, now)).toBe('—');
  });
});

describe('successRate', () => {
  it('counts the attempts with no failure', () => {
    const c = counts({ attempts: 8, failures: { email_timeout: 1, challenge: 2 } });
    expect(successRate(c)).toBe('62.5%');
  });
  it('is a dash at 0 attempts', () => {
    expect(successRate(counts())).toBe('—');
  });
});

describe('failedOfAttempts', () => {
  it.each([
    [
      counts({ attempts: 31, failures: { challenge: 5, email_timeout: 2 } }),
      '7 failed of 31 attempts',
    ],
    [counts({ attempts: 1 }), '0 failed of 1 attempt'],
    [counts(), '0 failed of 0 attempts'],
  ])('gives %o as %s', (c, text) => {
    expect(failedOfAttempts(c)).toBe(text);
  });
});

describe('failuresByCategory', () => {
  it('lists each category in name order', () => {
    const c = counts({ failures: { rate_limited: 4, challenge: 2, email_timeout: 1 } });
    expect(failuresByCategory(c)).toEqual([
      { category: 'challenge', count: 2 },
      { category: 'email_timeout', count: 1 },
      { category: 'rate_limited', count: 4 },
    ]);
  });
});

describe('bytesPerAccount', () => {
  it('divides the four totals by the accounts', () => {
    const c = counts({
      accounts: 2,
      browser_bytes_sent: 1000,
      browser_bytes_received: 400000,
      http_bytes_sent: 30000,
      http_bytes_received: 2000000,
    });
    expect(bytesPerAccount(c)).toBe('1.2MiB');
  });
  it('rounds to a whole byte', () => {
    expect(bytesPerAccount(counts({ accounts: 2, http_bytes_received: 1001 }))).toBe('501B');
  });
  it('is a dash at 0 accounts', () => {
    expect(bytesPerAccount(counts({ http_bytes_received: 5 }))).toBe('—');
  });
});

describe('byteTotals', () => {
  it('sums each phase in both directions', () => {
    const c = counts({
      browser_bytes_sent: 1024,
      browser_bytes_received: 2048,
      http_bytes_sent: 100,
      http_bytes_received: 400,
    });
    expect(byteTotals(c)).toBe('3.0KiB browser, 500B HTTP in total');
  });
});
