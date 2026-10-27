<script lang="ts">
  import { apiClient } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import Badge from '#lib/ui/Badge.svelte';
  import Button from '#lib/ui/Button.svelte';

  type EmailConfig = components['schemas']['EmailConfig'];
  type Result = components['schemas']['MailboxCheck'];
  type Login = components['schemas']['MailboxLogin'];

  interface Props {
    /** The edited email section. The check logs in with it, so a save is not necessary first. */
    email: EmailConfig;
    fetch: typeof globalThis.fetch;
    /** Turns the button off, for an email section that the check cannot log in with. */
    disabled?: boolean;
    /** Removes the mailboxes of users from the list. With none, the check shows no remove button. */
    onremove?: (users: string[]) => void;
  }

  const { email, fetch, disabled = false, onremove }: Props = $props();

  // With a mailboxes list, the check logs in to each mailbox of the list in place of the section.
  const listed = $derived(email.mailboxes?.length ?? 0);
  const label = $derived(
    listed === 0
      ? 'Check mailbox'
      : `Check ${String(listed)} ${listed === 1 ? 'mailbox' : 'mailboxes'}`,
  );

  let checking = $state(false);
  let result: Result | undefined = $state();
  // The logins that the list shows: all, the good ones or the failed ones.
  type Filter = 'all' | 'ok' | 'failed';
  let shown = $state<Filter>('all');

  const logins = $derived(result?.mailboxes ?? []);
  const good = $derived(logins.filter((l) => l.ok));
  const failed = $derived(logins.filter((l) => !l.ok));
  const visible = $derived(shown === 'ok' ? good : shown === 'failed' ? failed : logins);

  async function check(): Promise<void> {
    checking = true;
    result = undefined;
    shown = 'all';
    try {
      const { data, error } = await apiClient(fetch).POST('/api/checks/mailbox', { body: email });
      result = error === undefined ? data : { ok: false, error: error.message };
    } catch (e) {
      result = { ok: false, error: String(e) };
    }
    checking = false;
  }

  // A second click on the shown filter shows all the logins again.
  function show(filter: 'ok' | 'failed'): void {
    shown = shown === filter ? 'all' : filter;
  }

  // The logins left are the good ones, so the result becomes a pass.
  function removeFailed(): void {
    onremove?.(failed.map((l) => l.imap_user));
    result = { ok: true, mailboxes: good };
    shown = 'all';
  }

  function loginText(login: Login): string {
    return login.error === undefined ? login.imap_user : `${login.imap_user}: ${login.error}`;
  }
</script>

<div class="flex flex-wrap items-center gap-gap">
  <Button variant="secondary" disabled={disabled || checking} onclick={check}>{label}</Button>
  {#if result !== undefined}
    {#if result.ok}
      <p role="status" class="text-sm text-success">
        {result.mailboxes === undefined
          ? 'Login OK.'
          : `All ${String(result.mailboxes.length)} logins OK.`}
      </p>
    {:else}
      <p role="alert" class="text-sm text-danger">Mailbox check failed: {result.error}</p>
    {/if}
    {#if result.mailboxes !== undefined}
      <div class="flex w-full flex-wrap items-center gap-1.5">
        <Button
          variant="ghost"
          aria-pressed={shown === 'ok'}
          onclick={() => {
            show('ok');
          }}>OK ({good.length})</Button
        >
        <Button
          variant="ghost"
          aria-pressed={shown === 'failed'}
          onclick={() => {
            show('failed');
          }}>Failed ({failed.length})</Button
        >
        {#if onremove !== undefined && failed.length > 0}
          <Button variant="danger" onclick={removeFailed}>Remove failed ({failed.length})</Button>
        {/if}
      </div>
      <ul
        aria-label="Mailbox logins"
        class="flex max-h-72 w-full flex-col divide-y divide-line overflow-y-auto rounded-md border border-line"
      >
        {#each visible as login (login.imap_user)}
          <li class="flex flex-wrap items-center gap-2 px-3 py-2 text-sm">
            <Badge tone={login.ok ? 'success' : 'danger'}>{login.ok ? 'OK' : 'Failed'}</Badge>
            <span class="break-all" class:text-danger={!login.ok}>{loginText(login)}</span>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
</div>
