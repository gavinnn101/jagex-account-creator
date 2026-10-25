<script lang="ts">
  import { invalidate } from '$app/navigation';
  import { resolve } from '$app/paths';
  import LiveCode from '#lib/accounts/LiveCode.svelte';
  import Secret from '#lib/accounts/Secret.svelte';
  import { apiClient } from '#lib/api.ts';
  import { clock } from '#lib/clock.svelte.ts';
  import { formatTraffic, fullTime } from '#lib/format.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import Button from '#lib/ui/Button.svelte';
  import RelativeTime from '#lib/ui/RelativeTime.svelte';
  import type { PageProps } from './$types';

  const { data }: PageProps = $props();
  const account = $derived(data.account);
  // The relative time moves on in steps of a minute, so a tick each 30 s is enough.
  const time = clock(30_000);

  let confirming = $state(false);
  let confirmError = $state('');

  // The URL that load fetches, so load reads the account again.
  function reload(): Promise<void> {
    return invalidate(`/api/accounts/${String(account.id)}`);
  }

  async function confirm(): Promise<void> {
    confirming = true;
    confirmError = '';
    try {
      const { error } = await apiClient(fetch).POST('/api/accounts/{id}/confirm', {
        params: { path: { id: account.id } },
      });
      if (error === undefined) {
        await reload();
      } else {
        confirmError = error.message;
      }
    } catch (e) {
      confirmError = String(e);
    }
    confirming = false;
  }
</script>

<p class="mb-gap"><a class="text-accent underline" href={resolve('/accounts')}>All accounts</a></p>

<header class="mb-section flex flex-wrap items-center gap-x-4 gap-y-2">
  <h1 class="text-2xl font-bold">Account {account.id}</h1>
  <div class="flex flex-wrap items-center gap-2">
    {#if account.unconfirmed === true}
      <Badge tone="warning">Unconfirmed</Badge>
    {:else}
      <Badge tone="neutral">Confirmed</Badge>
    {/if}
    <Badge tone={account.totp ? 'success' : 'muted'}>{account.totp ? '2FA on' : '2FA off'}</Badge>
  </div>
  {#if account.unconfirmed === true}
    <div class="flex flex-wrap items-center gap-gap sm:ml-auto">
      <p class="text-sm text-muted">Check it by hand first.</p>
      <Button variant="primary" disabled={confirming} onclick={confirm}>Mark as checked</Button>
    </div>
  {/if}
</header>

<div class="flex flex-col gap-section">
  {#if confirmError !== ''}
    <p role="alert" class="text-danger">Confirm failed: {confirmError}</p>
  {/if}

  <div class="grid items-start gap-section lg:grid-cols-2">
    <section aria-labelledby="account-login" class="flex flex-col gap-gap">
      <h2 id="account-login" class="text-lg font-semibold">Login</h2>
      <dl class="term-grid items-center gap-x-6 gap-y-2">
        <dt class="font-semibold">Email</dt>
        <!-- A long email breaks at the @ before it breaks inside a word. -->
        <dd>
          {#each account.email.split('@') as part, i (i)}{#if i > 0}<wbr />@{/if}{part}{/each}
        </dd>
        <dt class="font-semibold">Username</dt>
        <dd>{account.username}</dd>
        <dt class="font-semibold">Password</dt>
        <dd><Secret label="password" value={account.password} /></dd>
        {#if account.totp_code !== undefined && account.totp_remaining_seconds !== undefined}
          <dt class="font-semibold">TOTP code</dt>
          <dd>
            <!-- Each read starts a new countdown, also when the seconds equal the last ones. -->
            {#key account}
              <LiveCode
                code={account.totp_code}
                seconds={account.totp_remaining_seconds}
                onExpire={() => void reload()}
              />
            {/key}
          </dd>
        {:else if account.totp_secret !== undefined}
          <dt class="font-semibold">TOTP code</dt>
          <dd>none, the secret does not decode</dd>
        {/if}
        {#if account.totp_secret !== undefined}
          <dt class="font-semibold">TOTP secret</dt>
          <dd><Secret label="TOTP secret" value={account.totp_secret} /></dd>
        {/if}
        {#if account.backup_codes !== undefined}
          <dt class="font-semibold">Backup codes</dt>
          <dd><Secret label="backup codes" value={account.backup_codes.join(' ')} /></dd>
        {/if}
      </dl>
    </section>

    <section aria-labelledby="account-origin" class="flex flex-col gap-gap">
      <h2 id="account-origin" class="text-lg font-semibold">Origin</h2>
      <dl class="term-grid gap-x-6 gap-y-2">
        <dt class="font-semibold">Created</dt>
        <dd>
          {fullTime(account.created_at)}
          <span class="text-muted">(<RelativeTime iso={account.created_at} now={time.now} />)</span>
        </dd>
        <dt class="font-semibold">Run</dt>
        <dd>
          {#if account.run_id === undefined}
            None
          {:else}
            <a
              class="text-accent underline"
              href={resolve('/runs/[id]', { id: String(account.run_id) })}>Run {account.run_id}</a
            >
          {/if}
        </dd>
        <dt class="font-semibold">Proxy</dt>
        <dd>{account.proxy ?? 'None'}</dd>
        <dt class="font-semibold">Browser</dt>
        <dd>{formatTraffic(account.browser_bytes_sent, account.browser_bytes_received)}</dd>
        <dt class="font-semibold">HTTP</dt>
        <dd>{formatTraffic(account.http_bytes_sent, account.http_bytes_received)}</dd>
      </dl>
    </section>
  </div>
</div>
