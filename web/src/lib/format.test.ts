import { describe, expect, it } from 'vitest';
import {
  clockTime,
  elapsedSince,
  formatBytes,
  formatTraffic,
  fullTime,
  percent,
  relativeTime,
  remainingUntil,
  tickTime,
} from './format.ts';

describe('formatBytes', () => {
  it.each([
    [0, '0B'],
    [1023, '1023B'],
    [1024, '1.0KiB'],
    [543210, '530.5KiB'],
    [1048524, '1023.9KiB'],
    [1048575, '1.0MiB'],
    [5 * 2 ** 30, '5.0GiB'],
    [2 ** 40, '1.0TiB'],
    [2 ** 50, '1.0PiB'],
    [2 ** 60, '1.0EiB'],
  ])('formats %d as %s', (n, text) => {
    expect(formatBytes(n)).toBe(text);
  });
});

describe('percent', () => {
  it.each([
    [2, 3, '66.7%'],
    [0, 4, '0.0%'],
    [4, 4, '100.0%'],
    [0, 0, '—'],
  ])('gives %d of %d as %s', (part, whole, text) => {
    expect(percent(part, whole)).toBe(text);
  });
});

describe('elapsedSince', () => {
  const now = new Date('2026-10-21T10:00:00.000Z');
  it.each([
    ['2026-10-21T09:59:54.500Z', '0:05'],
    ['2026-10-21T09:47:30.000Z', '12:30'],
    ['2026-10-21T08:57:55.000Z', '1:02:05'],
    ['2026-10-21T10:00:03.000Z', '0:00'],
  ])('gives the time from %s to now as %s', (since, text) => {
    expect(elapsedSince(since, now)).toBe(text);
  });
});

describe('remainingUntil', () => {
  const now = new Date('2026-10-21T10:00:00.000Z');
  it.each([
    ['2026-10-21T10:00:04.500Z', '0:05'],
    ['2026-10-21T10:00:00.200Z', '0:01'],
    ['2026-10-21T10:12:30.000Z', '12:30'],
    ['2026-10-21T11:02:05.000Z', '1:02:05'],
    ['2026-10-21T10:00:00.000Z', '0:00'],
    ['2026-10-21T09:59:57.000Z', '0:00'],
  ])('gives the time from now to %s as %s', (until, text) => {
    expect(remainingUntil(until, now)).toBe(text);
  });
});

describe('relativeTime', () => {
  const now = new Date('2026-10-21T10:00:00.000Z');
  const second = 1000;
  const minute = 60 * second;
  const hour = 60 * minute;
  const day = 24 * hour;
  // The time ms before now.
  const before = (ms: number) => new Date(now.getTime() - ms).toISOString();

  it.each([
    ['the same time', 0, 'now'],
    ['just below 60 s', minute - 1, 'now'],
    ['60 s', minute, '1m ago'],
    ['90 s', 90 * second, '1m ago'],
    ['just below 60 min', hour - 1, '59m ago'],
    ['60 min', hour, '1h ago'],
    ['just below 24 h', day - 1, '23h ago'],
    ['24 h', day, '1d ago'],
    ['just below 30 d', 30 * day - 1, '29d ago'],
    ['30 d', 30 * day, '1mo ago'],
    ['just below 365 d', 365 * day - 1, '12mo ago'],
    ['365 d', 365 * day, '1y ago'],
    ['800 d', 800 * day, '2y ago'],
  ])('gives a time %s before now as %s', (_, ms, text) => {
    expect(relativeTime(before(ms), now)).toBe(text);
  });

  it.each([
    ['5 s', 5 * second],
    ['2 d', 2 * day],
  ])('gives a time %s after now as now', (_, ms) => {
    expect(relativeTime(before(-ms), now)).toBe('now');
  });

  it('reads a time with 7 fraction digits', () => {
    expect(relativeTime('2026-10-21T09:55:00.1234567Z', now)).toBe('4m ago');
  });
});

describe('fullTime', () => {
  // The test browser runs in Asia/Kolkata, at GMT+5:30.
  it.each([
    ['2026-10-20T09:03:11Z', 'Oct 20, 2026, 2:33:11 PM GMT+5:30'],
    ['2026-01-04T19:05:07.1234567Z', 'Jan 5, 2026, 12:35:07 AM GMT+5:30'],
  ])('gives %s as %s', (iso, text) => {
    expect(fullTime(iso)).toBe(text);
  });
});

describe('clockTime', () => {
  it.each([
    ['2026-10-24T16:47:11.417+05:30', '16:47:11.417'],
    ['2026-10-24T16:47:11+05:30', '16:47:11'],
    ['2026-10-24T11:17:11.4171234Z', '11:17:11.4171234'],
    ['2026-10-24T16:47:11.', '16:47:11'],
    ['2026-10-24T6:47:11+05:30', '2026-10-24T6:47:11+05:30'],
    ['2026-10-24T16:4:11+05:30', '2026-10-24T16:4:11+05:30'],
    ['2026-10-24T16:47:1+05:30', '2026-10-24T16:47:1+05:30'],
    ['2026-10-24 16:47:11', '2026-10-24 16:47:11'],
    ['not a time', 'not a time'],
    ['', ''],
  ])('gives %j as %j', (text, clock) => {
    expect(clockTime(text)).toBe(clock);
  });
});

describe('formatTraffic', () => {
  it.each([
    [1024, 543210, '1.0KiB sent, 530.5KiB received'],
    [512, undefined, '512B sent, 0B received'],
    [undefined, 2048, '0B sent, 2.0KiB received'],
    [undefined, undefined, '0B sent, 0B received'],
  ])('gives %s sent and %s received as %s', (sent, received, text) => {
    expect(formatTraffic(sent, received)).toBe(text);
  });
});

describe('tickTime', () => {
  // Local times, and Wed Oct 21 2026. The test browser runs in Asia/Kolkata.
  it.each([
    ['a second', new Date(2026, 9, 21, 16, 25, 15), ':15'],
    ['a second below 10', new Date(2026, 9, 21, 16, 25, 5), ':05'],
    ['a minute', new Date(2026, 9, 21, 16, 25), '4:25 PM'],
    ['a minute below 10', new Date(2026, 9, 21, 9, 5), '9:05 AM'],
    ['an hour', new Date(2026, 9, 21, 16), '4 PM'],
    ['noon', new Date(2026, 9, 21, 12), '12 PM'],
    ['a day', new Date(2026, 9, 21), 'Wed 21'],
    ['a Sunday', new Date(2026, 9, 18), 'Oct 18'],
    ['a month that starts on a Sunday', new Date(2026, 10, 1), 'November'],
    ['a month', new Date(2026, 9, 1), 'October'],
    ['a year', new Date(2026, 0, 1), '2026'],
  ])('labels %s', (_, date, label) => {
    expect(tickTime(date)).toBe(label);
  });
});
