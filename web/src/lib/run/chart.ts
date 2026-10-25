import { scaleLinear, scaleTime, type ScaleTime } from 'd3-scale';
import {
  timeDay,
  timeHour,
  timeMinute,
  timeMonth,
  timeSecond,
  timeWeek,
  timeYear,
  type CountableTimeInterval,
} from 'd3-time';
import { tickTime } from '#lib/format.ts';
import type { RunSeries, SeriesPoint } from './attempts.ts';

/** One axis tick: its offset in the plot area in whole pixels, and its label. */
interface ChartTick {
  offset: number;
  label: string;
}

/** The shapes of the run chart in a plot area whose top left corner is 0,0. */
export interface ChartGeometry {
  /** The time ticks along the x axis, from the left. */
  xTicks: ChartTick[];
  /** The count ticks along the y axis, from the bottom. */
  yTicks: ChartTick[];
  /** The SVG path of the saved accounts. */
  accounts: string;
  /** The SVG path of the failed attempts. */
  failures: string;
}

const yTickCount = 5;
// The room that one time label needs on the x axis.
const xTickSpacing = 90;

// The round time steps, from coarse to fine.
const roundSteps: [CountableTimeInterval, number][] = [
  [timeYear, 1],
  [timeMonth, 3],
  [timeMonth, 1],
  [timeWeek, 1],
  [timeDay, 2],
  [timeDay, 1],
  [timeHour, 12],
  [timeHour, 6],
  [timeHour, 3],
  [timeHour, 2],
  [timeHour, 1],
  [timeMinute, 30],
  [timeMinute, 15],
  [timeMinute, 10],
  [timeMinute, 5],
  [timeMinute, 2],
  [timeMinute, 1],
  [timeSecond, 30],
  [timeSecond, 15],
  [timeSecond, 10],
  [timeSecond, 5],
  [timeSecond, 2],
  [timeSecond, 1],
];

/**
 * Returns the ticks of the finest round step that gives at most maxTicks ticks. When those are
 * fewer than two, or when even the year step gives too many, it returns every k-th tick of the
 * step with too many, at most maxTicks of them.
 */
function roundTicks(x: ScaleTime<number, number>, maxTicks: number): Date[] {
  // A finer step gives no fewer ticks, so go from coarse to fine and stop at the first misfit.
  // The year step reads no fit, so fit needs no start value.
  let fit!: Date[];
  for (const [index, [interval, step]] of roundSteps.entries()) {
    // every gives null only for a step below 1.
    const ticks = x.ticks(interval.every(step) ?? interval);
    if (ticks.length > maxTicks) {
      // Every k-th tick of a round step is on a round step too, so the thinned ticks stay round.
      const k = Math.ceil(ticks.length / maxTicks);
      return index === 0 || fit.length < 2 ? ticks.filter((_, i) => i % k === 0) : fit;
    }
    fit = ticks;
  }
  return fit;
}

/**
 * Returns the ticks and the two step paths of series over the run time from
 * start to end, in a plot area of width by height. Each path starts at a count
 * of 0 at start, steps up at each point, and runs to end.
 */
export function chartGeometry(
  series: RunSeries,
  start: Date,
  end: Date,
  width: number,
  height: number,
): ChartGeometry {
  const x = scaleTime().domain([start, end]).rangeRound([0, width]);
  const top = Math.max(
    1,
    ...series.accounts.map((p) => p.count),
    ...series.failures.map((p) => p.count),
  );
  const y = scaleLinear().domain([0, top]).nice(yTickCount).rangeRound([height, 0]);
  const path = (points: SeriesPoint[]) => {
    const steps = points.map((p) => `H${String(x(p.time))}V${String(y(p.count))}`);
    return `M${String(x(start))},${String(y(0))}${steps.join('')}H${String(x(end))}`;
  };
  const maxTicks = Math.max(2, Math.floor(width / xTickSpacing));
  return {
    xTicks: roundTicks(x, maxTicks).map((t) => ({ offset: x(t), label: tickTime(t) })),
    // A count is whole, so a tick between two counts has no use.
    yTicks: y
      .ticks(yTickCount)
      .filter((n) => Number.isInteger(n))
      .map((n) => ({ offset: y(n), label: String(n) })),
    accounts: path(series.accounts),
    failures: path(series.failures),
  };
}
