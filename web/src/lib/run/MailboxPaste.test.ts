import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { configFixture, editedContext } from '#lib/configtest/configtest.svelte.ts';
import { RunStream, runStreamKey } from '#lib/run/stream.ts';
import { lastSource, runState, stubEventSource } from '#lib/streamtest/streamtest.ts';
import { outlookIMAPHost, outlookTokenURL } from './mailboxLine.ts';
import MailboxPaste from './MailboxPaste.svelte';
import { pasted } from './pasted.svelte.ts';

const clientId = '9e5f94bc-e8a4-4e73-b8be-63364c29d753';
const token = `M.C552_BAY.0.U.-Ck${'a1B2*c3!D4$'.repeat(20)}`;
const line = (user: string, t = token): string => `${user}:hunter2:${t}:${clientId}`;

// The mailbox of the run that a pasted line gives, over the host of the config fixture.
const listed = (user: string, refreshToken: string) => ({
  imap_host: 'mail.example.com',
  imap_port: 993,
  imap_user: user,
  imap_domains: [],
  auth: 'oauth2' as const,
  oauth2: { client_id: clientId, refresh_token: refreshToken, token_url: outlookTokenURL },
});

// A serve whose store holds an account for each email of accounts, with its id.
// It records the emails of each lookup.
function lookupFetch(accounts: Record<string, number> = {}) {
  const lookups: string[][] = [];
  const fetch = async (input: RequestInfo | URL): Promise<Response> => {
    const { emails } = (await new Request(input).json()) as { emails: string[] };
    lookups.push(emails);
    const found = emails.flatMap((email) => {
      const id = accounts[email];
      return id === undefined ? [] : [{ email, id }];
    });
    return Response.json({ accounts: found });
  };
  return { fetch, lookups };
}

// Renders the paste box over a state copy of a saved config, changed by edit.
async function renderPaste(
  edit?: (config: ReturnType<typeof configFixture>) => void,
  accounts?: Record<string, number>,
) {
  const saved = configFixture();
  edit?.(saved);
  const { config, context } = editedContext(saved);
  const { fetch, lookups } = lookupFetch(accounts);
  const screen = await render(MailboxPaste, { props: { fetch }, context });
  return { screen, config, lookups, input: screen.getByLabelText('Paste mailbox lines') };
}

describe('MailboxPaste', () => {
  // The list outlives a render, as it outlives a visit to another screen, so each test starts with none.
  beforeEach(() => {
    pasted.text = '';
    pasted.picked = '';
    pasted.known = [];
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('gets the list and the picked mailbox back on a new render over a new config', async () => {
    const first = await renderPaste();
    await first.input.fill(
      [line('one@outlook.com'), line('two@outlook.com', `${token}2`)].join('\n'),
    );
    await first.screen.getByRole('button', { name: 'two@outlook.com', exact: true }).click();
    await first.screen.unmount();

    // The Run screen makes a new edited config from the saved one on each visit.
    const { screen, config, input } = await renderPaste();
    await expect
      .element(input)
      .toHaveValue([line('one@outlook.com'), line('two@outlook.com', `${token}2`)].join('\n'));
    await expect
      .element(screen.getByRole('button', { name: 'two@outlook.com', exact: true }))
      .toHaveAttribute('aria-pressed', 'true');
    expect(config.email.imap_user).toBe('two@outlook.com');
    expect(config.email.oauth2.refresh_token).toBe(`${token}2`);
  });

  it('shows a text area that describes the format, with no list yet', async () => {
    const { screen, input } = await renderPaste();
    await expect.element(input).toHaveProperty('tagName', 'TEXTAREA');
    await expect
      .element(input)
      .toHaveAccessibleDescription(
        'email:password:refresh_token:client_id, in any order, split by :, | or ----. The password is not used.',
      );
    expect(screen.getByRole('button').query()).toBeNull();
    expect(screen.getByRole('status').query()).toBeNull();
    expect(screen.getByRole('alert').query()).toBeNull();
  });

  it('fills the OAuth2 login of one line at once, with no pick list', async () => {
    const { screen, config, input } = await renderPaste();
    await input.fill(line('someone@outlook.com'));

    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent(
        '1 mailbox, one account at each. The login fields hold someone@outlook.com.',
      );
    await expect.element(input).toHaveValue(line('someone@outlook.com'));
    expect(screen.getByRole('list', { name: 'Pasted mailboxes' }).query()).toBeNull();
    expect(config.email).toMatchObject({
      auth: 'oauth2',
      imap_user: 'someone@outlook.com',
      imap_host: 'mail.example.com',
      oauth2: { client_id: clientId, refresh_token: token, token_url: outlookTokenURL },
    });
    expect(JSON.stringify(config)).not.toContain('hunter2');
  });

  it('lists the mailboxes of a pasted list, fills the first, and fills the picked one', async () => {
    const { screen, config, input } = await renderPaste();
    const lines = [
      line('one@outlook.com'),
      '',
      line('two@outlook.com', `${token}2`),
      line('three@outlook.com'),
    ];
    await input.fill(lines.join('\n'));

    const list = screen.getByRole('list', { name: 'Pasted mailboxes' });
    await expect.element(list).toBeVisible();
    expect(
      list
        .getByRole('button')
        .elements()
        .map((b) => [
          b.getAttribute('aria-label') ?? b.textContent.trim(),
          b.getAttribute('aria-pressed'),
        ]),
    ).toEqual([
      ['one@outlook.com', 'true'],
      ['Remove one@outlook.com', null],
      ['two@outlook.com', 'false'],
      ['Remove two@outlook.com', null],
      ['three@outlook.com', 'false'],
      ['Remove three@outlook.com', null],
    ]);
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent(
        '3 mailboxes, one account at each. The login fields hold one@outlook.com.',
      );
    expect(config.email.imap_user).toBe('one@outlook.com');

    expect(config.email.mailboxes).toEqual([
      listed('one@outlook.com', token),
      listed('two@outlook.com', `${token}2`),
      listed('three@outlook.com', token),
    ]);

    await list.getByRole('button', { name: 'two@outlook.com', exact: true }).click();
    await expect
      .element(list.getByRole('button', { name: 'two@outlook.com', exact: true }))
      .toHaveAttribute('aria-pressed', 'true');
    await expect
      .element(list.getByRole('button', { name: 'one@outlook.com', exact: true }))
      .toHaveAttribute('aria-pressed', 'false');
    expect(config.email.imap_user).toBe('two@outlook.com');
    expect(config.email.oauth2.refresh_token).toBe(`${token}2`);
  });

  it('keeps the picked mailbox when the list changes and still holds it', async () => {
    const { screen, config, input } = await renderPaste();
    const two = [line('one@outlook.com'), line('two@outlook.com')];
    await input.fill(two.join('\n'));
    await screen.getByRole('button', { name: 'two@outlook.com', exact: true }).click();
    await input.fill([...two, line('three@outlook.com')].join('\n'));
    await expect
      .element(screen.getByRole('button', { name: 'three@outlook.com', exact: true }))
      .toBeVisible();
    expect(config.email.imap_user).toBe('two@outlook.com');
  });

  it('removes one mailbox with the × of its pill, and its flag', async () => {
    const { screen, config, input } = await renderPaste(undefined, { 'one@outlook.com': 3 });
    await input.fill(
      [line('one@outlook.com'), line('two@outlook.com'), line('three@outlook.com')].join('\n'),
    );
    await expect.element(screen.getByRole('alert')).toBeVisible();

    await screen.getByRole('button', { name: 'Remove one@outlook.com' }).click();
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent(
        '2 mailboxes, one account at each. The login fields hold two@outlook.com.',
      );
    expect(input.element()).toHaveValue(
      [line('two@outlook.com'), line('three@outlook.com')].join('\n'),
    );
    expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual([
      'two@outlook.com',
      'three@outlook.com',
    ]);
    expect(config.email.imap_user).toBe('two@outlook.com');
    expect(screen.getByText('A run skips them', { exact: false }).query()).toBeNull();

    await screen.getByRole('button', { name: 'Remove three@outlook.com' }).click();
    await expect.element(input).toHaveValue(line('two@outlook.com'));
    expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual(['two@outlook.com']);
  });

  it('sets the Outlook host and port only where the section has no host', async () => {
    const { screen, config, input } = await renderPaste((c) => {
      c.email.imap_host = '';
      c.email.imap_port = 143;
      c.email.oauth2.token_url = 'https://oauth2.example.com/token';
    });
    await input.fill(line('someone@outlook.com'));

    await expect.element(screen.getByRole('status')).toBeVisible();
    expect(config.email.imap_host).toBe(outlookIMAPHost);
    expect(config.email.imap_port).toBe(993);
    expect(config.email.oauth2.token_url).toBe('https://oauth2.example.com/token');
  });

  it('names each line that does not read, lists the rest, and changes nothing for none', async () => {
    const { screen, config, input } = await renderPaste();
    const before = JSON.stringify(config);
    await input.fill(`someone@outlook.com:hunter2:${clientId}`);
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Line 1: The line holds no refresh token.');
    expect(screen.getByRole('status').query()).toBeNull();
    expect(JSON.stringify(config)).toBe(before);

    await input.fill([line('one@outlook.com'), 'junk', line('two@outlook.com')].join('\n'));
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Line 2: The line holds no email address.');
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent(
        '2 mailboxes, one account at each. The login fields hold one@outlook.com.',
      );
  });

  it('clears the list, the run mailboxes and the messages on an emptied text', async () => {
    const { screen, config, input } = await renderPaste();
    await input.fill(`${line('one@outlook.com')}\nnot a line`);
    await expect.element(screen.getByRole('alert')).toBeVisible();
    expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual(['one@outlook.com']);
    await input.fill('  ');
    await expect.element(screen.getByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByRole('status').query()).toBeNull();
    expect(config.email).not.toHaveProperty('mailboxes');
  });

  it('flags the pasted mailboxes that have an account, and removes them', async () => {
    const { screen, config, lookups, input } = await renderPaste(undefined, {
      'two@outlook.com': 7,
      'three@outlook.com': 9,
    });
    await input.fill(
      [line('one@outlook.com'), line('two@outlook.com'), line('three@outlook.com')].join('\n'),
    );

    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('2 mailboxes already have an account. A run skips them.');
    const links = screen.getByRole('list', { name: 'Mailboxes with an account' }).getByRole('link');
    expect(
      links
        .elements()
        .map((a) => [
          a.textContent.trim(),
          new URL(a.getAttribute('href') ?? '', location.href).pathname,
        ]),
    ).toEqual([
      ['two@outlook.com', '/accounts/7'],
      ['three@outlook.com', '/accounts/9'],
    ]);
    expect(lookups.at(-1)).toEqual(['one@outlook.com', 'two@outlook.com', 'three@outlook.com']);

    await screen.getByRole('button', { name: 'Remove them (2)' }).click();
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('1 mailbox, one account at each. The login fields hold one@outlook.com.');
    expect(input.element()).toHaveValue(line('one@outlook.com'));
    expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual(['one@outlook.com']);
    await expect.element(screen.getByRole('alert')).not.toBeInTheDocument();
  });

  it('names one mailbox with an account in the singular', async () => {
    const { screen, input } = await renderPaste(undefined, { 'one@outlook.com': 1 });
    await input.fill(line('one@outlook.com'));
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('1 mailbox already has an account. A run skips them.');
  });

  it('flags nothing when the lookup fails', async () => {
    const saved = configFixture();
    const { context } = editedContext(saved);
    const fetch = () => Promise.reject(new TypeError('Failed to fetch'));
    const screen = await render(MailboxPaste, { props: { fetch }, context });
    await screen.getByLabelText('Paste mailbox lines').fill(line('one@outlook.com'));
    await expect.element(screen.getByRole('status')).toBeVisible();
    expect(screen.getByRole('alert').query()).toBeNull();
  });

  it('keeps the mailboxes of the saved config when it opens with no pasted text', async () => {
    const saved = [listed('saved@outlook.com', token)];
    const { config } = await renderPaste((c) => {
      c.email.mailboxes = saved;
    });
    expect(config.email.mailboxes).toEqual(saved);
  });

  describe('with the run state stream', () => {
    // Renders the paste box with an open run state stream, over a serve whose store holds accounts.
    // The test can add an account to the store, as a run does, and send the next run state.
    async function renderWithStream(
      accounts: Record<string, number>,
      fetch = lookupFetch(accounts),
    ) {
      stubEventSource();
      const stream = new RunStream();
      stream.open();
      const source = lastSource();
      const { config, context } = editedContext(configFixture());
      const screen = await render(MailboxPaste, {
        props: { fetch: fetch.fetch },
        context: new Map<symbol, unknown>([...context, [runStreamKey, stream]]),
      });
      source.emit(runState('running'));
      return {
        screen,
        config,
        source,
        lookups: fetch.lookups,
        input: screen.getByLabelText('Paste mailbox lines'),
      };
    }

    const status = (n: number, login: string) =>
      `${String(n)} ${n === 1 ? 'mailbox' : 'mailboxes'}, one account at each. The login fields hold ${login}.`;

    it('removes a pasted mailbox once the run saves its account', async () => {
      const accounts: Record<string, number> = {};
      const { screen, config, source, input } = await renderWithStream(accounts);
      await input.fill([line('one@outlook.com'), line('two@outlook.com')].join('\n'));
      await expect
        .element(screen.getByRole('status'))
        .toHaveTextContent(status(2, 'one@outlook.com'));

      accounts['two@outlook.com'] = 9;
      source.emit(runState('running', { counts: { ...runState('running').counts, accounts: 1 } }));

      await expect
        .element(
          screen.getByText('Removed two@outlook.com: its account is on the Accounts screen.'),
        )
        .toBeVisible();
      await expect
        .element(screen.getByRole('status'))
        .toHaveTextContent(status(1, 'one@outlook.com'));
      expect(input.element()).toHaveValue(line('one@outlook.com'));
      expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual(['one@outlook.com']);
      expect(screen.getByRole('alert').query()).toBeNull();
    });

    it('asks nothing again on a state with the same account count', async () => {
      const { source, lookups, input } = await renderWithStream({});
      await input.fill(line('one@outlook.com'));
      await expect.poll(() => lookups.length).toBe(1);
      source.emit(runState('running', { workers: [] }));
      source.emit(runState('ended'));
      await new Promise((r) => setTimeout(r, 50));
      expect(lookups).toHaveLength(1);
    });

    it('keeps flagging a mailbox that had its account before the paste', async () => {
      const accounts: Record<string, number> = { 'two@outlook.com': 4 };
      const { screen, source, input } = await renderWithStream(accounts);
      await input.fill([line('one@outlook.com'), line('two@outlook.com')].join('\n'));
      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent('1 mailbox already has an account. A run skips them.');

      accounts['one@outlook.com'] = 5;
      source.emit(runState('running', { counts: { ...runState('running').counts, accounts: 1 } }));

      await expect
        .element(
          screen.getByText('Removed one@outlook.com: its account is on the Accounts screen.'),
        )
        .toBeVisible();
      await expect
        .element(screen.getByRole('status'))
        .toHaveTextContent(status(1, 'two@outlook.com'));
      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent('1 mailbox already has an account. A run skips them.');
    });

    it('marks the emails of a paste whose ask a run state replaced', async () => {
      // Each ask waits until the test answers it, so the test sets the order of the answers.
      const answers: ((emails: string[]) => void)[] = [];
      const lookups: string[][] = [];
      const fetch = async (input: RequestInfo | URL): Promise<Response> => {
        const { emails } = (await new Request(input).json()) as { emails: string[] };
        lookups.push(emails);
        return new Promise((resolve) => {
          answers.push((found) => {
            resolve(Response.json({ accounts: found.map((email, i) => ({ email, id: i + 1 })) }));
          });
        });
      };
      const { screen, source, input } = await renderWithStream({}, { fetch, lookups });
      await input.fill(line('one@outlook.com'));
      await expect.poll(() => answers.length).toBe(1);

      source.emit(runState('running', { counts: { ...runState('running').counts, accounts: 1 } }));
      await expect.poll(() => answers.length).toBe(2);
      answers[1]?.(['one@outlook.com']);
      answers[0]?.(['one@outlook.com']);

      await expect
        .element(screen.getByRole('alert'))
        .toHaveTextContent('1 mailbox already has an account. A run skips them.');
      await expect
        .element(screen.getByRole('status'))
        .toHaveTextContent(status(1, 'one@outlook.com'));
    });

    it('removes a mailbox whose account a run made while the screen was closed', async () => {
      pasted.text = [line('one@outlook.com'), line('two@outlook.com')].join('\n');
      const { screen, config } = await renderWithStream({ 'one@outlook.com': 3 });
      await expect
        .element(
          screen.getByText('Removed one@outlook.com: its account is on the Accounts screen.'),
        )
        .toBeVisible();
      expect(pasted.text).toBe(line('two@outlook.com'));
      expect(config.email.mailboxes?.map((m) => m.imap_user)).toEqual(['two@outlook.com']);
    });

    it('names two removed mailboxes in the plural', async () => {
      pasted.text = [
        line('one@outlook.com'),
        line('two@outlook.com'),
        line('three@outlook.com'),
      ].join('\n');
      const { screen } = await renderWithStream({ 'one@outlook.com': 3, 'two@outlook.com': 4 });
      await expect
        .element(
          screen.getByText(
            'Removed one@outlook.com, two@outlook.com: their accounts are on the Accounts screen.',
          ),
        )
        .toBeVisible();
    });
  });
});
