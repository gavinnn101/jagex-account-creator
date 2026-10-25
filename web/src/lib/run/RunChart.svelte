<script lang="ts">
  import type { RunSeries } from './attempts.ts';
  import { chartGeometry } from './chart.ts';

  interface Props {
    series: RunSeries;
    /** The start of the run. */
    start: Date;
    /** The end of the run, or now while it runs. */
    end: Date;
  }

  // Limitation: the chart misses an attempt whose row save failed, so it undercounts and never overcounts.
  const { series, start, end }: Props = $props();

  // The room around the plot area for the axis labels. An SVG clips its content,
  // so the right side holds half of a time label that sits at the end of the axis.
  const margin = { top: 8, right: 28, bottom: 32, left: 44 };
  const plotHeight = 200;
  const height = margin.top + plotHeight + margin.bottom;
  // The SVG draws at the width of its box with no view box, so the labels keep their CSS size.
  let width = $state(0);
  const plotWidth = $derived(width - margin.left - margin.right);
  const geometry = $derived(chartGeometry(series, start, end, plotWidth, plotHeight));
  const accounts = $derived(series.accounts.at(-1)?.count ?? 0);
  const failures = $derived(series.failures.at(-1)?.count ?? 0);
</script>

<figure class="flex flex-col gap-2 rounded-md border border-line bg-surface p-3">
  <figcaption class="font-semibold">Accounts and failures over the run</figcaption>
  <div bind:clientWidth={width} style:height="{height}px">
    <!-- The box has no width until its first measure. -->
    {#if width > 0}
      <svg
        {width}
        {height}
        role="img"
        aria-label="Saved accounts: {accounts}. Failed attempts: {failures}."
      >
        <g transform="translate({margin.left} {margin.top})">
          <g class="fill-muted text-xs">
            {#each geometry.yTicks as tick (tick.offset)}
              <g transform="translate(0 {tick.offset})">
                <line x2={plotWidth} class="stroke-line" />
                <text x="-6" dy="0.32em" text-anchor="end">{tick.label}</text>
              </g>
            {/each}
            {#each geometry.xTicks as tick (tick.offset)}
              <g transform="translate({tick.offset} {plotHeight})">
                <line y2="6" class="stroke-muted" />
                <text y="20" text-anchor="middle">{tick.label}</text>
              </g>
            {/each}
          </g>
          <path d={geometry.failures} fill="none" stroke-width="2" class="stroke-chart-failures" />
          <path d={geometry.accounts} fill="none" stroke-width="2" class="stroke-chart-accounts" />
        </g>
      </svg>
    {/if}
  </div>
  <ul class="flex gap-section text-sm" aria-label="Legend">
    <li class="flex items-center gap-2">
      <span class="inline-block h-1 w-4 bg-chart-accounts"></span>
      Saved accounts: {accounts}
    </li>
    <li class="flex items-center gap-2">
      <span class="inline-block h-1 w-4 bg-chart-failures"></span>
      Failed attempts: {failures}
    </li>
  </ul>
</figure>
