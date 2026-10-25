<script lang="ts">
  import CirclePause from '@lucide/svelte/icons/circle-pause';
  import CirclePlay from '@lucide/svelte/icons/circle-play';
  import PlugZap from '@lucide/svelte/icons/plug-zap';
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import { onMount } from 'svelte';
  import { SvelteMap } from 'svelte/reactivity';
  import { invalidate } from '$app/navigation';
  import { resolve } from '$app/paths';
  import { filterQuery } from '#lib/accounts/query.ts';
  import { apiClient } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import { formatBytes, percent } from '#lib/format.ts';
  import ImportForm from '#lib/proxies/ImportForm.svelte';
  import { runLimited } from '#lib/proxies/pool.ts';
  import {
    cooldownText,
    cooldownsByProxy,
    proxyColumns,
    runEnded,
    testView,
    totalBytes,
    type TestState,
  } from '#lib/proxies/rows.ts';
  import { runStream } from '#lib/run/stream.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import Button from '#lib/ui/Button.svelte';
  import ConfirmButton from '#lib/ui/ConfirmButton.svelte';
  import Details from '#lib/ui/Details.svelte';
  import RelativeTime from '#lib/ui/RelativeTime.svelte';
  import Table from '#lib/ui/Table.svelte';
  import type { PageProps } from './$types';

  type Proxy = components['schemas']['Proxy'];
  type RunState = components['schemas']['RunState'];

  const { data }: PageProps = $props();
  const stream = runStream();

  // Each test takes up to 2 s, so a few in flight keep each request far below the write timeout of serve.
  const testLimit = 4;

  let run: RunState | undefined = $state();
  let reconnecting = $state(false);
  let actionError = $state('');
  let testingAll = $state(false);
  // The tick redraws the cooldown and attempt times and sends no request.
  const time = clock(1000);
  const tests = new SvelteMap<number, TestState>();

  const proxies = $derived(data.proxies);
  const enabledIds = $derived(proxies.filter((p) => p.enabled).map((p) => p.id));
  const proxyNoun = $derived(proxies.length === 1 ? 'proxy' : 'proxies');
  const cooldowns = $derived(cooldownsByProxy(run));
  const shown = $derived(proxyColumns(proxies, cooldowns, tests));
  // Test follows Proxy, so the result of a click shows next to the proxy in a wide table.
  const columns = $derived([
    'Proxy',
    ...(shown.test ? ['Test'] : []),
    ...(shown.health ? ['Attempts', 'Success rate', 'Bytes'] : []),
    ...(shown.lastAttempt ? ['Last attempt'] : []),
    ...(shown.lastFailure ? ['Last failure'] : []),
    ...(shown.cooldown ? ['Cooldown'] : []),
    'Actions',
  ]);

  // The URL that load fetches, so load reads the list again.
  function reload(): Promise<void> {
    return invalidate('/api/proxies');
  }

  // Sends one change, then reads the list again. A refused or failed change shows its error.
  async function change(call: () => Promise<{ error?: { message: string } }>): Promise<void> {
    actionError = '';
    try {
      const { error } = await call();
      if (error === undefined) {
        await reload();
      } else {
        actionError = error.message;
      }
    } catch (e) {
      actionError = String(e);
    }
  }

  function setEnabled(p: Proxy): Promise<void> {
    const client = apiClient(fetch);
    const params = { path: { id: p.id } };
    return change(() =>
      p.enabled
        ? client.POST('/api/proxies/{id}/disable', { params })
        : client.POST('/api/proxies/{id}/enable', { params }),
    );
  }

  function remove(id: number): Promise<void> {
    return change(() => apiClient(fetch).DELETE('/api/proxies/{id}', { params: { path: { id } } }));
  }

  async function test(id: number): Promise<void> {
    tests.set(id, { kind: 'running' });
    try {
      const { data: result, error } = await apiClient(fetch).POST('/api/proxies/{id}/test', {
        params: { path: { id } },
      });
      tests.set(
        id,
        error === undefined
          ? { kind: 'answered', result }
          : { kind: 'failed', message: error.message },
      );
    } catch (e) {
      tests.set(id, { kind: 'failed', message: String(e) });
    }
  }

  async function testAll(): Promise<void> {
    testingAll = true;
    await runLimited(enabledIds, testLimit, test);
    testingAll = false;
  }

  onMount(() => {
    let last: RunState['phase'] | undefined;
    return stream.subscribe({
      onMessage: (state) => {
        run = state;
        // One reload at the end of the run, not one for each attempt row.
        if (runEnded(last, state.phase)) {
          void reload();
        }
        last = state.phase;
      },
      onReconnecting: (value) => {
        reconnecting = value;
      },
    });
  });
</script>

<div class="mb-section flex flex-wrap items-center justify-between gap-gap">
  <h1 class="text-2xl font-bold">Proxies</h1>
  {#if proxies.length > 0}
    <div class="flex flex-wrap items-center gap-gap">
      <Button variant="secondary" disabled={testingAll} onclick={testAll}>Test all enabled</Button>
      <!-- The path is an API file, not a route, so the router leaves it to the browser. -->
      <a class="text-accent underline" href="/api/proxies/export" rel="external" download>Export</a>
    </div>
  {/if}
</div>

<div class="flex flex-col gap-section">
  {#if proxies.length === 0}
    <p>No proxies yet. Import a proxy file below.</p>
  {:else}
    <div class="flex flex-col gap-gap">
      <p class="tabular-nums">
        <span class="font-semibold">{proxies.length}</span>
        {proxyNoun},
        <span class="font-semibold">{enabledIds.length}</span> enabled. A change applies at the next start
        of a run.
      </p>
      {#if enabledIds.length === 0}
        <p class="flex items-center gap-1.5 text-warning">
          <TriangleAlert aria-hidden="true" class="size-4 shrink-0" />
          No proxy is enabled, so a run stops at start unless "Direct, with no proxy" is on.
        </p>
      {/if}
    </div>
  {/if}

  {#if reconnecting}
    <p role="status" class="text-danger">Reconnecting. The cooldowns below can be stale.</p>
  {/if}

  {#if actionError !== ''}
    <p role="alert" class="text-danger">Change failed: {actionError}</p>
  {/if}

  {#if proxies.length > 0}
    <Table caption="Proxies" {columns} stickyActions>
      {#each proxies as p (p.id)}
        {@const cooldown = cooldowns.get(p.proxy)}
        {@const state = tests.get(p.id)}
        {@const view = state === undefined ? undefined : testView(state)}
        {@const toggleName = `${p.enabled ? 'Disable' : 'Enable'} ${p.proxy}`}
        <tr class="whitespace-nowrap">
          <td>
            <span class="inline-flex items-center gap-2">
              <!-- The cell cuts a long host short, so the badges and the next columns stay in view. -->
              <a
                class="max-w-64 truncate text-accent hover:underline focus-visible:underline"
                title={p.proxy}
                href={resolve(`/accounts?${filterQuery({ proxy: p.proxy })}`)}>{p.proxy}</a
              >
              {#if p.auth}<Badge tone="neutral">Auth</Badge>{/if}
              {#if !p.enabled}<Badge tone="muted">Disabled</Badge>{/if}
            </span>
          </td>
          {#if shown.test}
            <td>
              {#if view !== undefined}
                <!-- The error names the cause, so it is the one text that wraps. A wide table gives each
                     column its narrowest width, so the error has a fixed width and not only a cap. -->
                <span class="inline-flex items-baseline gap-2">
                  <Badge tone={view.tone}>{view.label}</Badge>
                  {#if view.detail !== ''}
                    <span class="w-xs shrink-0 break-words whitespace-normal">{view.detail}</span>
                  {/if}
                </span>
              {/if}
            </td>
          {/if}
          {#if shown.health}
            <td>{p.attempts}</td>
            <td>{percent(p.successes, p.attempts)}</td>
            <td>{formatBytes(totalBytes(p))}</td>
          {/if}
          {#if shown.lastAttempt}
            <td>
              {#if p.last_attempt_at !== undefined}
                <RelativeTime iso={p.last_attempt_at} now={time.now} />
              {/if}
            </td>
          {/if}
          {#if shown.lastFailure}
            <td>{p.last_failure ?? ''}</td>
          {/if}
          {#if shown.cooldown}
            <td>
              {#if cooldown !== undefined}
                <Badge tone="warning">{cooldownText(cooldown, time.now)}</Badge>
              {/if}
            </td>
          {/if}
          <td>
            <div class="flex items-center gap-1">
              <Button
                variant="ghost"
                size="icon"
                aria-label={toggleName}
                title={toggleName}
                onclick={() => setEnabled(p)}
              >
                {#if p.enabled}
                  <CirclePause aria-hidden="true" class="size-4" />
                {:else}
                  <CirclePlay aria-hidden="true" class="size-4" />
                {/if}
              </Button>
              <Button
                variant="ghost"
                size="icon"
                aria-label="Test {p.proxy}"
                title="Test {p.proxy}"
                disabled={state?.kind === 'running'}
                onclick={() => test(p.id)}><PlugZap aria-hidden="true" class="size-4" /></Button
              >
              <ConfirmButton size="icon" name={p.proxy} onconfirm={() => remove(p.id)} />
            </div>
          </td>
        </tr>
      {/each}
    </Table>
  {/if}

  <Details summary="Import proxies" open={proxies.length === 0}>
    <ImportForm onImported={reload} />
  </Details>
</div>
