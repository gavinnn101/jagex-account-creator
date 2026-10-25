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
  imap_domain: 'example.com',
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
