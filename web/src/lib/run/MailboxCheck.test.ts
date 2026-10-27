import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import MailboxCheck from './MailboxCheck.svelte';

type EmailConfig = components['schemas']['EmailConfig'];

const email: EmailConfig = {
  imap_host: 'mail.example.com',
  imap_port: 993,
  imap_user: 'user@example.com',
  imap_pass: 'secret',
  imap_domains: ['example.com'],
  auth: 'password',
  oauth2: {
    client_id: '',
    token_url: '',
  },
};

interface Sent {
  method: string;
  path: string;
  body: string;
}

// Answers each request with the next reply, and records each request.
function fakeFetch(...replies: (() => Promise<Response>)[]) {
  const sent: Sent[] = [];
  const fetch = async (input: RequestInfo | URL): Promise<Response> => {
    const request = new Request(input);
    sent.push({
      method: request.method,
      path: new URL(request.url).pathname,
      body: await request.text(),
    });
    const reply = replies.shift();
    if (reply === undefined) throw new Error('no reply');
    return reply();
  };
  return { sent, fetch };
}

function json(status: number, body: unknown) {
  return () => Promise.resolve(Response.json(body, { status }));
}

async function renderCheck(...replies: (() => Promise<Response>)[]) {
  const { sent, fetch } = fakeFetch(...replies);
  const screen = await render(MailboxCheck, { email, fetch });
  const button = screen.getByRole('button', { name: 'Check mailbox', exact: true });
  const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
  const alert = () => screen.container.querySelector('[role="alert"]')?.textContent ?? null;
  return { screen, sent, button, status, alert };
}

describe('MailboxCheck', () => {
  it('posts the email section and shows a good login', async () => {
    const { screen, sent, button, alert } = await renderCheck(json(200, { ok: true }));
    expect(screen.container.querySelector('[role="status"]')).toBeNull();
    expect(alert()).toBeNull();
    await button.click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Login OK.');
    expect(alert()).toBeNull();
    expect(sent).toEqual([
      { method: 'POST', path: '/api/checks/mailbox', body: JSON.stringify(email) },
    ]);
  });

  it('shows the error of a failed login, and drops the last result', async () => {
    const { screen, button, status } = await renderCheck(
      json(200, { ok: true }),
      json(200, { ok: false, error: 'dial tcp 127.0.0.1:1: connection refused' }),
    );
    await button.click();
    await expect.element(screen.getByRole('status')).toBeVisible();
    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Mailbox check failed: dial tcp 127.0.0.1:1: connection refused');
    expect(status()).toBeNull();
  });

  it('shows the message of a refused body', async () => {
    const { screen, button, status } = await renderCheck(
      json(400, { message: 'request body has an error' }),
    );
    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Mailbox check failed: request body has an error');
    expect(status()).toBeNull();
  });

  it('shows a failed request, and clears it on the next check', async () => {
    const { screen, button, alert } = await renderCheck(
      () => Promise.reject(new TypeError('Failed to fetch')),
      json(200, { ok: true }),
    );
    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Mailbox check failed: TypeError: Failed to fetch');
    await button.click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Login OK.');
    expect(alert()).toBeNull();
  });

  it('checks each mailbox of a list, and shows the login of each', async () => {
    const mailbox = (user: string) => ({ ...email, imap_user: user, imap_domains: [] });
    const listed = { ...email, mailboxes: [mailbox('a@outlook.com'), mailbox('b@outlook.com')] };
    const { sent, fetch } = fakeFetch(
      json(200, {
        ok: false,
        error: '1 of 2 mailboxes failed the login',
        mailboxes: [
          { imap_user: 'a@outlook.com', ok: true },
          { imap_user: 'b@outlook.com', ok: false, error: 'login refused' },
        ],
      }),
      json(200, {
        ok: true,
        mailboxes: [
          { imap_user: 'a@outlook.com', ok: true },
          { imap_user: 'b@outlook.com', ok: true },
        ],
      }),
    );
    const screen = await render(MailboxCheck, { email: listed, fetch });
    const button = screen.getByRole('button', { name: 'Check 2 mailboxes', exact: true });
    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Mailbox check failed: 1 of 2 mailboxes failed the login');
    const logins = screen.getByRole('list', { name: 'Mailbox logins' });
    expect(
      logins
        .getByRole('listitem')
        .elements()
        .map((li) => li.textContent.replace(/\s+/g, ' ').trim()),
    ).toEqual(['OK a@outlook.com', 'Failed b@outlook.com: login refused']);
    expect(sent.map((r) => r.body)).toEqual([JSON.stringify(listed)]);

    await button.click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('All 2 logins OK.');
    expect(
      logins
        .getByRole('listitem')
        .elements()
        .map((li) => li.textContent.replace(/\s+/g, ' ').trim()),
    ).toEqual(['OK a@outlook.com', 'OK b@outlook.com']);
    expect(screen.getByRole('alert').query()).toBeNull();
  });

  it('shows the good or the failed logins, and removes the failed ones', async () => {
    const mailbox = (user: string) => ({ ...email, imap_user: user, imap_domains: [] });
    const listed = {
      ...email,
      mailboxes: [mailbox('a@outlook.com'), mailbox('b@outlook.com'), mailbox('c@outlook.com')],
    };
    const { fetch } = fakeFetch(
      json(200, {
        ok: false,
        error: '2 of 3 mailboxes failed the login',
        mailboxes: [
          { imap_user: 'a@outlook.com', ok: false, error: 'refused' },
          { imap_user: 'b@outlook.com', ok: true },
          { imap_user: 'c@outlook.com', ok: false, error: 'expired' },
        ],
      }),
    );
    const removed: string[][] = [];
    const screen = await render(MailboxCheck, {
      email: listed,
      fetch,
      onremove: (users: string[]) => removed.push(users),
    });
    const shownUsers = () =>
      screen
        .getByRole('list', { name: 'Mailbox logins' })
        .getByRole('listitem')
        .elements()
        .map((li) => li.textContent.replace(/\s+/g, ' ').trim());
    await screen.getByRole('button', { name: 'Check 3 mailboxes' }).click();
    const okButton = screen.getByRole('button', { name: 'OK (1)' });
    const failedButton = screen.getByRole('button', { name: 'Failed (2)' });
    await expect.element(okButton).toHaveAttribute('aria-pressed', 'false');
    expect(shownUsers()).toHaveLength(3);

    await okButton.click();
    await expect.element(okButton).toHaveAttribute('aria-pressed', 'true');
    expect(shownUsers()).toEqual(['OK b@outlook.com']);
    await failedButton.click();
    await expect.element(failedButton).toHaveAttribute('aria-pressed', 'true');
    await expect.element(okButton).toHaveAttribute('aria-pressed', 'false');
    expect(shownUsers()).toEqual([
      'Failed a@outlook.com: refused',
      'Failed c@outlook.com: expired',
    ]);
    await failedButton.click();
    await expect.element(failedButton).toHaveAttribute('aria-pressed', 'false');
    expect(shownUsers()).toHaveLength(3);

    await failedButton.click();
    await screen.getByRole('button', { name: 'Remove failed (2)' }).click();
    expect(removed).toEqual([['a@outlook.com', 'c@outlook.com']]);
    await expect.element(screen.getByRole('status')).toHaveTextContent('All 1 logins OK.');
    expect(shownUsers()).toEqual(['OK b@outlook.com']);
    await expect
      .element(screen.getByRole('button', { name: 'Failed (0)' }))
      .toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByRole('button', { name: /^Remove failed/ }).query()).toBeNull();
  });

  it('shows no remove button with no onremove', async () => {
    const listed = { ...email, mailboxes: [{ ...email, imap_domains: [] }] };
    const { fetch } = fakeFetch(
      json(200, {
        ok: false,
        error: '1 of 1 mailboxes failed the login',
        mailboxes: [{ imap_user: 'user@example.com', ok: false, error: 'refused' }],
      }),
    );
    const screen = await render(MailboxCheck, { email: listed, fetch });
    await screen.getByRole('button', { name: 'Check 1 mailbox' }).click();
    await expect.element(screen.getByRole('button', { name: 'Failed (1)' })).toBeVisible();
    expect(screen.getByRole('button', { name: /^Remove failed/ }).query()).toBeNull();
  });

  it('names one listed mailbox in the singular', async () => {
    const { fetch } = fakeFetch();
    const screen = await render(MailboxCheck, {
      email: { ...email, mailboxes: [{ ...email, imap_domains: [] }] },
      fetch,
    });
    await expect
      .element(screen.getByRole('button', { name: 'Check 1 mailbox', exact: true }))
      .toBeVisible();
  });

  it('keeps the button off when the parent turns it off', async () => {
    const { sent, fetch } = fakeFetch();
    const screen = await render(MailboxCheck, { email, fetch, disabled: true });
    const button = screen.getByRole('button', { name: 'Check mailbox', exact: true });
    await expect.element(button).toBeDisabled();
    await screen.rerender({ disabled: false });
    await expect.element(button).toBeEnabled();
    expect(sent).toEqual([]);
  });

  it('turns the button off while the check runs, and hides the last result', async () => {
    let answer: (r: Response) => void = () => undefined;
    const pending = () =>
      new Promise<Response>((resolve) => {
        answer = resolve;
      });
    const { button, status, alert } = await renderCheck(
      json(200, { ok: false, error: 'login refused' }),
      pending,
    );
    await button.click();
    await expect.poll(alert).toBe('Mailbox check failed: login refused');
    await button.click();
    await expect.element(button).toBeDisabled();
    expect(alert()).toBeNull();
    answer(Response.json({ ok: true }));
    await expect.element(button).toBeEnabled();
    expect(status()).toBe('Login OK.');
  });
});
