import '../../app.css';
import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { RunSeries } from './attempts.ts';
import { chartGeometry } from './chart.ts';
import RunChart from './RunChart.svelte';

// 3:30 PM to 3:40 PM in the zone of the test browser.
const start = new Date('2026-10-21T10:00:00.000Z');
const end = new Date('2026-10-21T10:10:00.000Z');

function at(minutes: number): number {
  return start.getTime() + minutes * 60_000;
}

const series: RunSeries = {
  accounts: [
    { time: at(2), count: 1 },
    { time: at(4), count: 2 },
    { time: at(7), count: 3 },
  ],
  failures: [{ time: at(5), count: 1 }],
};

// Renders the chart in a host of width, so the first measure of the chart has that width.
// The card takes 26 px of padding and border, so a 642 px host gives a 616 px SVG.
async function renderChart(width: string, props: { series: RunSeries; start: Date; end: Date }) {
  const target = document.createElement('div');
  target.style.width = width;
  document.body.append(target);
  const screen = await render(RunChart, { props, target });
  return { screen, target };
}

function transforms(svg: Element, selector: string): string[] {
  return [...svg.querySelectorAll(selector)].map((g) => g.getAttribute('transform') ?? '');
}

describe('RunChart', () => {
  it('draws both series at the width of its box and names the final values', async () => {
    const { screen } = await renderChart('642px', { series, start, end });
    const img = screen.getByRole('img', { name: 'Saved accounts: 3. Failed attempts: 1.' });
    await expect.element(img).toBeVisible();
    const svg = img.element();
    expect([svg.getAttribute('width'), svg.getAttribute('height')]).toEqual(['616', '240']);
    expect(svg.hasAttribute('viewBox')).toBe(false);
    // The plot area is 616 px less the 44 px left and the 28 px right margins.
    const geometry = chartGeometry(series, start, end, 544, 200);
    const paths = [...svg.querySelectorAll('path')].map((p) => [
      p.getAttribute('d'),
      p.getAttribute('class'),
    ]);
    expect(paths).toEqual([
      [geometry.failures, 'stroke-chart-failures'],
      [geometry.accounts, 'stroke-chart-accounts'],
    ]);
    const labels = [...svg.querySelectorAll('text')].map((t) => t.textContent);
    expect(labels).toEqual([
      ...geometry.yTicks.map((t) => t.label),
      ...geometry.xTicks.map((t) => t.label),
    ]);
    expect(geometry.xTicks.map((t) => t.label)).toEqual([
      '3:30 PM',
      '3:32 PM',
      '3:34 PM',
      '3:36 PM',
      '3:38 PM',
      '3:40 PM',
    ]);
    expect(transforms(svg, 'svg > g')).toEqual(['translate(44 8)']);
    const ticks = transforms(svg, 'g[transform] > g > g');
    expect(ticks).toEqual([
      ...geometry.yTicks.map((t) => `translate(0 ${String(t.offset)})`),
      ...geometry.xTicks.map((t) => `translate(${String(t.offset)} 200)`),
    ]);
    const grid = [...svg.querySelectorAll('line[x2]')].map((l) => l.getAttribute('x2'));
    expect(grid).toEqual(geometry.yTicks.map(() => '544'));
    expect(geometry.yTicks.length).toBeGreaterThan(1);
    const legend = screen.getByRole('list', { name: 'Legend' });
    await expect.element(legend).toMatchTextContent('Saved accounts: 3');
    await expect.element(legend).toMatchTextContent('Failed attempts: 1');
  });

  it('draws nothing in a box of no width, then redraws at each new width', async () => {
    const { screen, target } = await renderChart('0px', { series, start, end });
    await expect.element(screen.getByRole('list', { name: 'Legend' })).toBeInTheDocument();
    // A frame gives the box its first measure.
    await new Promise((resolve) => requestAnimationFrame(resolve));
    expect(screen.container.querySelector('svg')).toBeNull();

    const svg = () => screen.container.querySelector('svg');
    const label = () => svg()?.querySelector('text')?.getBoundingClientRect().height;
    target.style.width = '342px';
    await expect.poll(() => svg()?.getAttribute('width')).toBe('316');
    const narrow = chartGeometry(series, start, end, 244, 200);
    const drawn = () =>
      [...(svg()?.querySelectorAll('path') ?? [])].map((p) => p.getAttribute('d'));
    expect(drawn()).toEqual([narrow.failures, narrow.accounts]);
    expect(svg()?.querySelector('line[x2]')?.getAttribute('x2')).toBe('244');
    const narrowLabel = label();
    expect(narrowLabel).toBeGreaterThan(0);

    target.style.width = '1242px';
    await expect.poll(() => svg()?.getAttribute('width')).toBe('1216');
    const wide = chartGeometry(series, start, end, 1144, 200);
    expect(drawn()).toEqual([wide.failures, wide.accounts]);
    // A label keeps its size at each width.
    expect(label()).toBe(narrowLabel);
  });

  it('names zero values for an empty run', async () => {
    const empty: RunSeries = { accounts: [], failures: [] };
    const { screen } = await renderChart('642px', { series: empty, start, end });
    await expect
      .element(screen.getByRole('img', { name: 'Saved accounts: 0. Failed attempts: 0.' }))
      .toBeVisible();
  });
});
