import { describe, expect, it } from 'vitest';
import type { RunSeries } from './attempts.ts';
import { chartGeometry } from './chart.ts';

// Local time, so the tick labels hold in each time zone.
const start = new Date(2026, 9, 21, 10, 0, 0);
const end = new Date(start.getTime() + 100_000);
const none: RunSeries = { accounts: [], failures: [] };

function at(seconds: number, count: number) {
  return { time: start.getTime() + seconds * 1000, count };
}

// The labels of the time ticks of a run from first to last in a plot area of width.
function runLabels(first: Date, last: Date, width: number): string[] {
  return chartGeometry(none, first, last, width, 200).xTicks.map((t) => t.label);
}

// The labels of the time ticks of a run of seconds from start in a plot area of width.
function xLabels(seconds: number, width: number): string[] {
  return runLabels(start, new Date(start.getTime() + seconds * 1000), width);
}

describe('chartGeometry', () => {
  it('steps each series up at its points', () => {
    const series: RunSeries = {
      accounts: [at(10, 1), at(30, 2), at(60, 3), at(90, 4)],
      failures: [at(20, 1), at(70, 2)],
    };
    const g = chartGeometry(series, start, end, 1000, 200);
    expect(g.accounts).toBe('M0,200H100V150H300V100H600V50H900V0H1000');
    expect(g.failures).toBe('M0,200H200V150H700V100H1000');
    expect(g.yTicks).toEqual([
      { offset: 200, label: '0' },
      { offset: 150, label: '1' },
      { offset: 100, label: '2' },
      { offset: 50, label: '3' },
      { offset: 0, label: '4' },
    ]);
    expect(g.xTicks).toEqual([
      { offset: 0, label: '10 AM' },
      { offset: 100, label: ':10' },
      { offset: 200, label: ':20' },
      { offset: 300, label: ':30' },
      { offset: 400, label: ':40' },
      { offset: 500, label: ':50' },
      { offset: 600, label: '10:01 AM' },
      { offset: 700, label: ':10' },
      { offset: 800, label: ':20' },
      { offset: 900, label: ':30' },
      { offset: 1000, label: ':40' },
    ]);
  });

  it('rounds the count axis up to a nice top', () => {
    const series: RunSeries = {
      accounts: [],
      failures: [1, 2, 3, 4, 5, 6, 7, 8, 9].map((n) => at(n, n)),
    };
    const g = chartGeometry(series, start, end, 1000, 200);
    expect(g.yTicks.map((t) => [t.offset, t.label])).toEqual([
      [200, '0'],
      [160, '2'],
      [120, '4'],
      [80, '6'],
      [40, '8'],
      [0, '10'],
    ]);
  });

  it('draws flat paths and whole count ticks with no points', () => {
    const g = chartGeometry(none, start, end, 1000, 200);
    expect(g.accounts).toBe('M0,200H1000');
    expect(g.failures).toBe('M0,200H1000');
    expect(g.yTicks).toEqual([
      { offset: 200, label: '0' },
      { offset: 0, label: '1' },
    ]);
  });

  it('labels the hours and the minutes of a long run on a 12-hour clock', () => {
    expect(xLabels(2 * 3600, 544)).toEqual(['10 AM', '10:30 AM', '11 AM', '11:30 AM', '12 PM']);
  });

  it('labels the days of a run of several days', () => {
    expect(xLabels(3 * 86_400, 544)).toEqual([
      '12 PM',
      'Thu 22',
      '12 PM',
      'Fri 23',
      '12 PM',
      'Sat 24',
    ]);
  });

  it('labels the years of a run of several years', () => {
    expect(xLabels(10 * 365 * 86_400, 1134)).toEqual([
      '2027',
      '2028',
      '2029',
      '2030',
      '2031',
      '2032',
      '2033',
      '2034',
      '2035',
      '2036',
    ]);
  });

  it('thins the year ticks when even the year step gives too many', () => {
    expect(xLabels(10 * 365 * 86_400, 260)).toEqual(['2027', '2032']);
  });

  it('keeps the time ticks to one for each 90 px, on a round step', () => {
    // A count of 12 gives d3 27 ticks one minute apart, and a label needs more than 42 px.
    const labels = xLabels(26.5 * 60, 1134);
    expect(labels.length).toBeLessThanOrEqual(Math.floor(1134 / 90));
    expect(labels).toEqual(['10 AM', '10:05 AM', '10:10 AM', '10:15 AM', '10:20 AM', '10:25 AM']);
  });

  it('keeps as many time ticks as fit', () => {
    // 11 ticks one minute apart fit exactly in 990 px.
    expect(xLabels(10 * 60, 990)).toHaveLength(11);
  });

  it('keeps each label of a run below a minute apart', () => {
    expect(xLabels(40, 540)).toEqual(['10 AM', ':10', ':20', ':30', ':40']);
    expect(xLabels(20, 544)).toEqual(['10 AM', ':05', ':10', ':15', ':20']);
    expect(xLabels(2, 544)).toEqual(['10 AM', ':01', ':02']);
  });

  it('gives at most one time tick to each 90 px of the plot width', () => {
    expect(xLabels(40, 180)).toEqual(['10 AM', ':30']);
    expect(xLabels(10, 270)).toEqual(['10 AM', ':05', ':10']);
    expect(xLabels(60, 360)).toEqual(['10 AM', ':30', '10:01 AM']);
  });

  it('keeps two time ticks in a narrow plot area', () => {
    expect(xLabels(40, 100)).toEqual(['10 AM', ':30']);
  });

  it('keeps two round time ticks of a short run in a phone plot area', () => {
    // d3 has no step between 1 and 5 minutes, and a 5-minute tick misses this run.
    const last = new Date(2026, 9, 24, 22, 4, 30);
    const first = new Date(last.getTime() - 4.4 * 60_000);
    expect(runLabels(first, last, 260)).toEqual(['10:02 PM', '10:04 PM']);
  });

  it('thins the next finer step when the step that fits gives one tick', () => {
    const first = new Date(2026, 9, 24, 22, 0, 30);
    const last = new Date(2026, 9, 24, 22, 6, 0);
    expect(runLabels(first, last, 260)).toEqual(['10:02 PM', '10:06 PM']);
  });

  it('keeps at most two thinned labels of a 9.5-minute run in a phone plot area', () => {
    // The 5-minute step gives one tick, and the 2-minute step gives four.
    const last = new Date(2026, 9, 25, 0, 49, 51);
    const first = new Date(last.getTime() - 9.5 * 60_000);
    expect(runLabels(first, last, 260)).toEqual(['12:42 AM', '12:46 AM']);
  });

  it('keeps at most two thinned day labels of a 13.7-day run in a phone plot area', () => {
    // The week step gives one tick, and the 2-day step gives six.
    const last = new Date(2026, 9, 24, 22, 9, 20);
    const first = new Date(last.getTime() - 19_710 * 60_000);
    expect(runLabels(first, last, 260)).toEqual(['Tue 13', 'Mon 19']);
  });
});
