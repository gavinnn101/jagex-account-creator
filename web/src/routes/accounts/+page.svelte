<script lang="ts">
  import { resolve } from '$app/paths';
  import { exportHref, hasFilters, pageQuery, type ExportFormat } from '#lib/accounts/query.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import Button from '#lib/ui/Button.svelte';
  import Details from '#lib/ui/Details.svelte';
  import Field from '#lib/ui/Field.svelte';
  import RelativeTime from '#lib/ui/RelativeTime.svelte';
  import Select from '#lib/ui/Select.svelte';
  import Table from '#lib/ui/Table.svelte';
  import type { PageProps } from './$types';

  const { data }: PageProps = $props();
  const filters = $derived(data.filters);
  const accounts = $derived(data.page.accounts);
  const total = $derived(data.page.total);
  const nextBefore = $derived(data.page.next_before);
  const filtered = $derived(hasFilters(filters));
  const totalNoun = $derived(
    `${filtered ? 'matching ' : ''}${total === 1 ? 'account' : 'accounts'}`,
  );
  // The relative times move on in steps of a minute, so a tick each 30 s is enough.
  const time = clock(30_000);
  // A test can kill the mutant of this path in the script.
  const runHref = resolve('/');

  // Limitation: the rule reads one page, so a column can show on one page and hide on the next.
  const runColumn = $derived(accounts.some((a) => a.run_id !== undefined));
  const proxyColumn = $derived(accounts.some((a) => a.proxy !== undefined));
  const columns = $derived([
    'ID',
    'Created',
    'Email',
    'Username',
    ...(runColumn ? ['Run'] : []),
    ...(proxyColumn ? ['Proxy'] : []),
    '2FA',
    'Status',
  ]);

  // The empty value selects every account.
  const yesNo = [
    { value: '', text: 'Any' },
    { value: 'true', text: 'Yes' },
    { value: 'false', text: 'No' },
  ];

  const exports: { format: ExportFormat; text: string }[] = [
    { format: 'jsonl', text: 'JSONL' },
    { format: 'csv', text: 'CSV' },
    { format: 'userpass', text: 'email:password' },
  ];
</script>

<h1 class="mb-section text-2xl font-bold">Accounts</h1>

<div class="flex flex-col gap-section">
  <p>
    <a class="text-accent underline" href={resolve('/accounts?unconfirmed=true')}
      >Unconfirmed accounts</a
    >: the accounts to check by hand.
  </p>

  <form aria-label="Account filters" method="GET" class="flex flex-col gap-gap">
    <div class="flex flex-wrap items-end gap-gap">
      <div class="min-w-0 flex-[1_1_16rem]">
        <Field label="Email or username" name="q" type="search" value={filters.q} />
      </div>
      <div class="w-28">
        <Select
          label="Unconfirmed"
          name="unconfirmed"
          options={yesNo}
          value={filters.unconfirmed}
        />
      </div>
      <div class="w-28">
        <Select label="2FA" name="totp" options={yesNo} value={filters.totp} />
      </div>
      <div class="flex items-center gap-gap">
        <Button variant="primary" type="submit">Filter</Button>
        <a class="inline-flex h-8 items-center text-accent underline" href={resolve('/accounts')}
          >Clear</a
        >
      </div>
    </div>
    <!-- A closed section still sends its fields with the form. -->
    <Details
      summary="More filters"
      open={hasFilters({
        run: filters.run,
        proxy: filters.proxy,
        created_from: filters.created_from,
        created_to: filters.created_to,
      })}
    >
      <!-- Four columns only from xl, so a date field keeps the 233 px that shows its AM/PM. -->
      <div class="grid gap-gap sm:grid-cols-2 xl:grid-cols-4">
        <Field label="Run" name="run" type="number" min="1" value={filters.run} />
        <Field label="Proxy" name="proxy" value={filters.proxy} />
        <Field
          label="Created from"
          name="created_from"
          type="datetime-local"
          value={filters.created_from}
        />
        <Field
          label="Created before"
          name="created_to"
          type="datetime-local"
          value={filters.created_to}
        />
      </div>
    </Details>
  </form>

  <!-- Limitation: a before below every matching id gives a total above 0 and an empty page.
       It fails toward a bare total line, never toward wrong data. -->
  {#if total === 0}
    {#if filtered}
      <p>
        No account matches these filters.
        <a class="text-accent underline" href={resolve('/accounts')}>Clear the filters</a>
      </p>
    {:else}
      <p>
        No accounts yet.
        <a class="text-accent underline" href={runHref}>Start a run</a>
      </p>
    {/if}
  {:else}
    <div class="flex flex-wrap items-baseline justify-between gap-gap">
      <p class="tabular-nums"><span class="font-semibold">{total}</span> {totalNoun}</p>
      <section aria-labelledby="accounts-export" class="flex flex-wrap items-baseline gap-gap">
        <h2 id="accounts-export" class="text-sm font-semibold text-muted">Export</h2>
        <ul class="flex gap-gap">
          {#each exports as { format, text } (format)}
            <li>
              <!-- The path is an API file, not a route, so the router leaves it to the browser. -->
              <a
                class="text-accent underline"
                href={exportHref(filters, format)}
                rel="external"
                download>{text}</a
              >
            </li>
          {/each}
        </ul>
      </section>
    </div>

    {#if accounts.length > 0}
      <Table caption="Accounts, newest first" {columns}>
        {#each accounts as account (account.id)}
          <tr class="whitespace-nowrap">
            <td>{account.id}</td>
            <td><RelativeTime iso={account.created_at} now={time.now} /></td>
            <td>
              <a
                class="text-accent hover:underline focus-visible:underline"
                href={resolve('/accounts/[id]', { id: String(account.id) })}>{account.email}</a
              >
            </td>
            <td>{account.username}</td>
            {#if runColumn}
              <td>{account.run_id ?? ''}</td>
            {/if}
            {#if proxyColumn}
              <td>{account.proxy ?? ''}</td>
            {/if}
            <td>
              <Badge tone={account.totp ? 'success' : 'muted'}>{account.totp ? 'On' : 'Off'}</Badge>
            </td>
            <td>
              {#if account.unconfirmed === true}
                <Badge tone="warning">Unconfirmed</Badge>
              {/if}
            </td>
          </tr>
        {/each}
      </Table>
    {/if}

    {#if nextBefore !== undefined}
      <p>
        <a
          class="text-accent underline"
          href={resolve(`/accounts?${pageQuery(filters, nextBefore)}`)}>Next page</a
        >
      </p>
    {/if}
  {/if}
</div>
