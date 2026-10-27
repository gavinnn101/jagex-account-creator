<script lang="ts">
  import TriangleAlert from '@lucide/svelte/icons/triangle-alert';
  import X from '@lucide/svelte/icons/x';
  import { onMount } from 'svelte';
  import { resolve } from '$app/paths';
  import { apiClient } from '#lib/api.ts';
  import type { components } from '#lib/api.gen.d.ts';
  import { editedConfig } from '#lib/run/edited.ts';
  import {
    outlookIMAPHost,
    outlookTokenURL,
    parseMailboxList,
    type MailboxLine,
    type MailboxList,
  } from '#lib/run/mailboxLine.ts';
  import { pasted, removeMailboxes } from '#lib/run/pasted.svelte.ts';
  import { runStream, type RunStream } from '#lib/run/stream.ts';
  import Button from '#lib/ui/Button.svelte';
  import TextArea from '#lib/ui/TextArea.svelte';

  type AccountMatch = components['schemas']['AccountMatch'];

  interface Props {
    /** The fetch of the lookup of the accounts of the pasted emails. */
    fetch: typeof globalThis.fetch;
  }

  const { fetch }: Props = $props();

  // The fill writes to the edited config.
  const config = editedConfig();

  // The list follows the text, also when the mailbox check removes the lines of failed logins.
  const list: MailboxList = $derived(parseMailboxList(pasted.text));

  // Each change of the text reads the list again and asks which of its emails have an account.
  function setText(value: string | undefined): void {
    applyText(value ?? '');
    void lookUp('paste');
  }

  // The first mailbox fills the fields at once, unless the picked one is still in
  // the list. Then the whole list becomes the mailboxes of the run. A text emptied
  // of its mailboxes drops that list, but the first read keeps the list of the saved config.
  function applyText(value: string): void {
    const hadMailboxes = list.mailboxes.length > 0;
    pasted.text = value;
    const picked = list.mailboxes.find((m) => m.user === pasted.picked);
    const first = list.mailboxes[0];
    if (picked !== undefined) {
      fill(picked);
    } else if (first !== undefined) {
      fill(first);
    }
    if (list.mailboxes.length > 0) {
      fillList(list.mailboxes);
    } else if (hadMailboxes) {
      delete config.email.mailboxes;
    }
  }

  // The screen makes a new edited config on each visit, so a return fills it from the kept list again.
  applyText(pasted.text);

  // A fill sets the OAuth2 login of the mailbox. The host and the token URL get the
  // values of Outlook only where the section has none, so a fill keeps a typed one.
  function fill(mailbox: MailboxLine): void {
    pasted.picked = mailbox.user;
    const { email } = config;
    email.auth = 'oauth2';
    email.imap_user = mailbox.user;
    email.oauth2.client_id = mailbox.clientId;
    email.oauth2.refresh_token = mailbox.refreshToken;
    if (email.imap_host === '') {
      email.imap_host = outlookIMAPHost;
      email.imap_port = 993;
    }
    if (email.oauth2.token_url === '') {
      email.oauth2.token_url = outlookTokenURL;
    }
  }

  // A run makes one account at each mailbox. Each takes the host, the port and the
  // token URL of the section, which a fill has set before.
  function fillList(mailboxes: readonly MailboxLine[]): void {
    const { email } = config;
    email.mailboxes = mailboxes.map((m) => ({
      imap_host: email.imap_host,
      imap_port: email.imap_port,
      imap_user: m.user,
      imap_domains: [],
      auth: 'oauth2',
      oauth2: {
        client_id: m.clientId,
        refresh_token: m.refreshToken,
        token_url: email.oauth2.token_url,
      },
    }));
  }

  // The pasted mailboxes whose address had an account when the user pasted them.
  // A mailbox with no domain makes one account, so a run skips them, and the box
  // flags them. Any other pasted mailbox that has an account got it from a run
  // since, so the box removes it at once. The answer for an older ask is dropped.
  let used: AccountMatch[] = $state.raw([]);
  let removed = $state('');
  let asked = 0;
  // A paste ask that a later ask replaced still owes the marks of its emails, so the next ask takes them on.
  let pasteDue = false;

  async function lookUp(mode: 'paste' | 'refresh'): Promise<void> {
    const emails = list.mailboxes.map((m) => m.user);
    pasteDue ||= mode === 'paste';
    const n = ++asked;
    let found: AccountMatch[] = [];
    if (emails.length > 0) {
      try {
        const { data } = await apiClient(fetch).POST('/api/accounts/lookup', { body: { emails } });
        found = data?.accounts ?? [];
      } catch {
        // A failed lookup flags nothing. The run still skips such a mailbox at its start.
      }
    }
    if (n !== asked) {
      return;
    }
    const isKnown = (m: AccountMatch) => pasted.known.includes(m.email.toLowerCase());
    if (pasteDue) {
      pasted.known = [
        ...pasted.known,
        ...found.filter((m) => !isKnown(m)).map((m) => m.email.toLowerCase()),
      ];
      pasteDue = false;
    }
    const made = found.filter((m) => !isKnown(m));
    if (made.length > 0) {
      removeMailboxes(
        config.email,
        made.map((m) => m.email),
      );
      const whose = made.length === 1 ? 'its account is' : 'their accounts are';
      removed = `Removed ${made.map((m) => m.email).join(', ')}: ${whose} on the Accounts screen.`;
    }
    used = found.filter(isKnown);
  }

  // Each saved account changes the counts of the run state, so the box asks again.
  // The stream is absent in a test that gives none.
  const stream = runStream() as RunStream | undefined;
  onMount(() => {
    void lookUp('refresh');
    let seen: number | undefined;
    return stream?.subscribe({
      onMessage: (state) => {
        const accounts = state.counts.accounts;
        if (seen !== undefined && accounts !== seen) {
          void lookUp('refresh');
        }
        seen = accounts;
      },
      onReconnecting: () => undefined,
    });
  });

  // The × of a pill removes its line from the text and its mailbox from the run, and drops its flag.
  function removeOne(user: string): void {
    removeMailboxes(config.email, [user]);
    used = used.filter((m) => m.email.toLowerCase() !== user.toLowerCase());
  }

  const usedNoun = $derived(used.length === 1 ? 'mailbox already has' : 'mailboxes already have');

  const mailboxNoun = $derived(list.mailboxes.length === 1 ? 'mailbox' : 'mailboxes');
</script>

<div class="flex flex-col gap-2">
  <TextArea
    label="Paste mailbox lines"
    rows={3}
    autocomplete="off"
    spellcheck="false"
    hint="email:password:refresh_token:client_id, in any order, split by :, | or ----. The password is not used."
    bind:value={() => pasted.text, setText}
  />
  {#if list.mailboxes.length > 0}
    <p role="status" class="text-sm text-muted">
      {list.mailboxes.length}
      {mailboxNoun}, one account at each. The login fields hold {config.email.imap_user}.
    </p>
    {#if list.mailboxes.length > 1}
      <ul
        aria-label="Pasted mailboxes"
        class="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto rounded-md border border-line bg-canvas/40 p-2"
      >
        {#each list.mailboxes as mailbox (mailbox.user)}
          {@const picked = mailbox.user === config.email.imap_user}
          <li class="flex items-center">
            <Button
              variant={picked ? 'primary' : 'secondary'}
              aria-pressed={picked}
              onclick={() => {
                fill(mailbox);
              }}>{mailbox.user}</Button
            >
            <Button
              variant="ghost"
              size="icon"
              aria-label={`Remove ${mailbox.user}`}
              onclick={() => {
                removeOne(mailbox.user);
              }}><X aria-hidden="true" class="size-4" /></Button
            >
          </li>
        {/each}
      </ul>
    {/if}
  {/if}
  {#if used.length > 0}
    <div
      class="flex flex-col gap-2 rounded-md border border-warning/30 bg-warning-soft px-3 py-2.5 text-sm"
    >
      <p role="alert" class="flex items-start gap-1.5 font-medium text-warning">
        <TriangleAlert aria-hidden="true" class="mt-0.5 size-4 shrink-0" />
        {used.length}
        {usedNoun} an account. A run skips them.
      </p>
      <ul aria-label="Mailboxes with an account" class="flex flex-wrap gap-x-3 gap-y-1">
        {#each used as match (match.email)}
          <li>
            <a
              class="break-all text-ink underline decoration-line-strong underline-offset-4 hover:decoration-current"
              href={resolve('/accounts/[id]', { id: String(match.id) })}>{match.email}</a
            >
          </li>
        {/each}
      </ul>
      <div>
        <Button
          variant="danger"
          onclick={() => {
            removeMailboxes(
              config.email,
              used.map((m) => m.email),
            );
            used = [];
          }}>Remove them ({used.length})</Button
        >
      </div>
    </div>
  {/if}
  {#if removed !== ''}
    <p aria-live="polite" class="text-sm text-success">{removed}</p>
  {/if}
  {#if list.errors.length > 0}
    <ul
      role="alert"
      class="rounded-md border border-danger/30 bg-danger-soft px-3 py-2 text-sm text-danger"
    >
      {#each list.errors as { line, error } (line)}
        <li>Line {line}: {error}</li>
      {/each}
    </ul>
  {/if}
</div>
