<script lang="ts">
  import {
    accountsDigest,
    chromeDigest,
    generalDigest,
    mailboxDigest,
    runDigest,
  } from '#lib/digest.ts';
  import { editedConfig } from '#lib/run/edited.ts';
  import MailboxCheck from '#lib/run/MailboxCheck.svelte';
  import Checkbox from '#lib/ui/Checkbox.svelte';
  import Details from '#lib/ui/Details.svelte';
  import Field from '#lib/ui/Field.svelte';

  interface Props {
    /** The fetch of the mailbox check. */
    fetch: typeof globalThis.fetch;
  }

  const { fetch }: Props = $props();
  // The fields write to the edited config.
  const config = editedConfig();

  // An empty number input gives null, and the contract types each number as a required integer.
  function orZero(value: number | null): number {
    return value ?? 0;
  }

  // The password is write-only: an empty field sends no imap_pass, and serve keeps its stored one.
  function setPass(value: string | null): void {
    if (!value) {
      delete config.email.imap_pass;
    } else {
      config.email.imap_pass = value;
    }
  }
</script>

<div class="flex flex-col gap-2">
  <Details summary="Run settings" digest={runDigest(config)} open>
    <div class="grid gap-gap sm:grid-cols-2 lg:grid-cols-4">
      <Field
        label="Account count"
        type="number"
        hint="-1: no limit."
        bind:value={
          () => config.concurrency.account_count,
          (v: number | null) => (config.concurrency.account_count = orZero(v))
        }
      />
      <Field
        label="Max workers"
        type="number"
        bind:value={
          () => config.concurrency.max_workers,
          (v: number | null) => (config.concurrency.max_workers = orZero(v))
        }
      />
      <Field
        label="Max browsers"
        type="number"
        bind:value={
          () => config.concurrency.max_browsers,
          (v: number | null) => (config.concurrency.max_browsers = orZero(v))
        }
      />
      <Field
        label="Account timeout"
        title="A Go duration, for example 1m. It bounds each attempt up to the password POST."
        bind:value={config.concurrency.account_timeout}
      />
      <div class="sm:col-span-2 lg:col-span-4">
        <Checkbox
          label="Direct, with no proxy"
          hint="A run with no enabled proxy fails unless this is on."
          bind:checked={config.proxy.direct}
        />
      </div>
    </div>
  </Details>

  <Details summary="Mailbox" digest={mailboxDigest(config)}>
    <div class="flex flex-col gap-gap">
      <div class="grid gap-gap sm:grid-cols-2 lg:grid-cols-3">
        <Field label="IMAP host" bind:value={config.email.imap_host} />
        <Field
          label="IMAP port"
          type="number"
          bind:value={
            () => config.email.imap_port, (v: number | null) => (config.email.imap_port = orZero(v))
          }
        />
        <Field label="IMAP user" autocomplete="off" bind:value={config.email.imap_user} />
        <Field
          label="IMAP password"
          type="password"
          autocomplete="new-password"
          placeholder={config.email.imap_pass_set ? 'Stored. Empty keeps it.' : undefined}
          bind:value={() => config.email.imap_pass ?? '', setPass}
        />
        <Field label="IMAP domain" bind:value={config.email.imap_domain} />
      </div>
      <MailboxCheck email={config.email} {fetch} />
    </div>
  </Details>

  <Details summary="Accounts" digest={accountsDigest(config)}>
    <div class="grid items-end gap-gap sm:grid-cols-2 lg:grid-cols-4">
      <Field
        label="Password length"
        type="number"
        bind:value={
          () => config.account.password_length,
          (v: number | null) => (config.account.password_length = orZero(v))
        }
      />
      <Checkbox label="Enable TOTP" bind:checked={config.account.enable_totp} />
    </div>
  </Details>

  <Details summary="Chrome" digest={chromeDigest(config)}>
    <div class="grid items-end gap-gap sm:grid-cols-2 lg:grid-cols-4">
      <Field
        label="Chrome timeout"
        title="A Go duration, for example 1m30s."
        bind:value={config.chrome.timeout}
      />
      <Checkbox label="Headless" bind:checked={config.chrome.headless} />
    </div>
  </Details>

  <Details summary="General" digest={generalDigest(config)}>
    <div class="grid gap-gap sm:grid-cols-2">
      <Field
        label="Database"
        readonly
        hint="Serve opens this database. A run must use it."
        value={config.database}
      />
      <Checkbox label="Debug" bind:checked={config.debug} />
    </div>
  </Details>
</div>
