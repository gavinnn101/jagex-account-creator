<script lang="ts">
  import { createVirtualizer } from '@tanstack/svelte-virtual';
  import { onMount } from 'svelte';
  import type { Attachment } from 'svelte/attachments';
  import { SvelteSet } from 'svelte/reactivity';
  import { get } from 'svelte/store';
  import type { components } from '#lib/api.gen.d.ts';
  import { clockTime, formatBytes } from '#lib/format.ts';

  type LogLine = components['schemas']['LogLine'];
  type LogAttr = components['schemas']['LogAttr'];

  interface Props {
    /** The lines to show, oldest first. */
    lines: LogLine[];
  }

  const { lines }: Props = $props();

  const stepPrefix = 'steps.';

  let scroller: HTMLDivElement | undefined = $state();
  const expanded = new SvelteSet<LogLine>();

  // At the end of the list, a new line scrolls into view, as tail -f does.
  const virtualizer = createVirtualizer<HTMLDivElement, HTMLLIElement>({
    count: 0,
    getScrollElement: () => scroller ?? null,
    estimateSize: () => 24,
    overscan: 10,
    anchorTo: 'end',
    followOnAppend: true,
  });

  // get() reads the store once, so the store update of setOptions does not run this effect again.
  $effect(() => {
    get(virtualizer).setOptions({ count: lines.length });
  });

  // The lines at mount come before the virtualizer has its scroll element, so it does not follow them.
  onMount(() => {
    get(virtualizer).scrollToEnd();
  });

  // The virtualizer observes each row, so a row that expands gets its new height.
  const measure: Attachment<HTMLLIElement> = (row) => {
    get(virtualizer).measureElement(row);
  };

  function steps(line: LogLine): LogAttr[] {
    return line.attrs.filter((a) => a.key.startsWith(stepPrefix));
  }

  function others(line: LogLine): LogAttr[] {
    return line.attrs.filter((a) => !a.key.startsWith(stepPrefix));
  }

  // The record line counts bytes in attributes such as browser_bytes_sent, as whole numbers.
  function bytesText(attr: LogAttr): string | undefined {
    return attr.key.includes('_bytes_') && /^\d+$/.test(attr.value)
      ? formatBytes(Number(attr.value))
      : undefined;
  }

  function toggle(line: LogLine): void {
    if (!expanded.delete(line)) {
      expanded.add(line);
    }
  }
</script>

<!-- The region scrolls, so the keyboard must be able to focus it. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div
  bind:this={scroller}
  role="region"
  aria-label="Log lines"
  tabindex="0"
  class="max-h-[75vh] overflow-auto rounded border border-line font-mono text-sm"
>
  <ul class="relative" style:height="{$virtualizer.getTotalSize()}px">
    {#each $virtualizer.getVirtualItems() as row (row.index)}
      {@const line = lines[row.index]}
      <!-- The rows can hold the old count for one render after the lines shrink. -->
      {#if line !== undefined}
        {@const stepAttrs = steps(line)}
        <li
          data-index={row.index}
          {@attach measure}
          class="absolute top-0 left-0 w-full px-2 py-0.5 break-words"
          style:transform="translateY({row.start}px)"
        >
          <time class="text-muted" datetime={line.time} title={line.time}
            >{clockTime(line.time)}</time
          >
          <!-- The data attribute picks the color. A static class string gives Stryker no mutant to survive. -->
          <span
            data-level={line.level}
            class="font-semibold data-[level=DEBUG]:text-muted data-[level=ERROR]:text-danger data-[level=WARN]:text-warning"
            >{line.level}</span
          >
          <!-- Each attribute brings its own leading space. -->
          <span>{line.msg}</span>{#each others(line) as attr, i (i)}{@const bytes =
              bytesText(attr)}<span
              ><span class="text-muted">{` ${attr.key}=`}</span
              >{#if bytes === undefined}{attr.value}{:else}<span title={attr.value}>{bytes}</span
                >{/if}</span
            >{/each}{#if stepAttrs.length > 0}
            <button
              type="button"
              class="ml-[1ch] text-accent underline"
              aria-expanded={expanded.has(line)}
              onclick={() => {
                toggle(line);
              }}>Steps</button
            >
            {#if expanded.has(line)}
              <dl class="term-grid gap-x-4 pl-4">
                {#each stepAttrs as step, i (i)}
                  <dt>{step.key.slice(stepPrefix.length)}</dt>
                  <dd>{step.value}</dd>
                {/each}
              </dl>
            {/if}
          {/if}
        </li>
      {/if}
    {/each}
  </ul>
</div>
