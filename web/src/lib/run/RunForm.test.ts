import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { configFixture, editedContext } from '#lib/configtest/configtest.svelte.ts';
import RunForm from './RunForm.svelte';

const initial = configFixture();

interface Sent {
  method: string;
  path: string;
  body: string;
}

// Answers the one request with reply, and records it.
function fakeFetch(reply: () => Promise<Response>) {
  const sent: Sent[] = [];
  const fetch = async (input: RequestInfo | URL): Promise<Response> => {
    const request = new Request(input);
    sent.push({
      method: request.method,
      path: new URL(request.url).pathname,
      body: await request.text(),
    });
    return reply();
  };
  return { sent, fetch };
}

function json(status: number, body: unknown) {
  return () => Promise.resolve(Response.json(body, { status }));
}

// Renders the form over a state copy of initial, as the page owns it.
async function renderForm(reply: () => Promise<Response>) {
  const { config, context } = editedContext(initial);
  const { sent, fetch } = fakeFetch(reply);
  const screen = await render(RunForm, { props: { fetch }, context });
  const save = screen.getByRole('button', { name: 'Save', exact: true });
  const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
  const pass = screen.getByLabelText('IMAP password');
  // The password sits in the closed Mailbox section, and a closed control takes no input.
  screen.getByText('Mailbox', { exact: true }).element().closest('summary')?.click();
  await expect.element(pass).toBeVisible();
  return { screen, config, sent, save, status, pass };
}

// Returns a reply that waits for answer, so a test sees the state while the request runs.
function held() {
  let answer: (r: Response) => void = () => undefined;
  const reply = () =>
    new Promise<Response>((resolve) => {
      answer = resolve;
    });
  return {
    reply,
    answer: (r: Response) => {
      answer(r);
    },
  };
}

describe('RunForm', () => {
  it('is the config region with its heading and the one primary Save', async () => {
    const { screen, save } = await renderForm(json(200, initial));
    await expect.element(screen.getByRole('form', { name: 'Run config' })).toBeVisible();
    await expect.element(screen.getByRole('heading', { name: 'Config' })).toBeVisible();
    await expect.element(save).toHaveAttribute('data-variant', 'primary');
    await expect.element(save).toBeEnabled();
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
    expect(screen.container.querySelectorAll('details')).toHaveLength(5);
  });

  it('saves the edited config and applies the answer to the fields', async () => {
    const { reply, answer } = held();
    const { screen, config, sent, save, status, pass } = await renderForm(reply);
    expect(pass.element()).not.toHaveAttribute('placeholder');
    await pass.fill('secret');
    await save.click();
    await expect.element(save).toBeDisabled();
    expect(status()).toBeNull();
    answer(Response.json({ ...initial, email: { ...initial.email, imap_pass_set: true } }));
    await expect.element(screen.getByRole('status')).toHaveTextContent('Config saved.');
    expect(save.element()).toBeEnabled();
    expect(pass.element()).toHaveValue('');
    expect(pass.element()).toHaveAttribute('placeholder', 'Stored. Empty keeps it.');
    expect(config.email.imap_pass_set).toBe(true);
    expect(sent).toEqual([
      {
        method: 'PUT',
        path: '/api/config',
        body: JSON.stringify({ ...initial, email: { ...initial.email, imap_pass: 'secret' } }),
      },
    ]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
    // The fields still edit the config after the save replaced its email section.
    await pass.fill('again');
    await expect.element(pass).toHaveValue('again');
    expect(config.email.imap_pass).toBe('again');
  });

  it.for<[number, unknown, string[]]>([
    [
      422,
      { valid: false, errors: ['email.imap_host must be set', 'database must be creator.db'] },
      ['email.imap_host must be set', 'database must be creator.db'],
    ],
    [400, { message: 'request body has an error' }, ['request body has an error']],
    [500, { message: 'writing config.local.yaml: denied' }, ['writing config.local.yaml: denied']],
  ])('shows the errors of a save with status %d', async ([code, body, errors]) => {
    const { screen, save, status, pass } = await renderForm(json(code, body));
    await pass.fill('secret');
    await save.click();
    const alert = screen.getByRole('alert', { name: 'Save errors' });
    await expect.element(alert).toBeVisible();
    expect(
      alert
        .getByRole('listitem')
        .elements()
        .map((li) => li.textContent),
    ).toEqual(errors);
    expect(status()).toBeNull();
    expect(pass.element()).toHaveValue('secret');
    await expect.element(save).toBeEnabled();
  });

  it('shows a failed save request', async () => {
    const { screen, save, status } = await renderForm(() =>
      Promise.reject(new TypeError('Failed to fetch')),
    );
    await save.click();
    await expect
      .element(screen.getByRole('alert', { name: 'Save errors' }))
      .toHaveTextContent('TypeError: Failed to fetch');
    expect(status()).toBeNull();
    await expect.element(save).toBeEnabled();
  });

  it('drops the saved status and the last errors on the next save', async () => {
    const replies = [json(200, initial), json(500, { message: 'disk full' }), json(200, initial)];
    const { screen, save, status } = await renderForm(() => {
      const reply = replies.shift();
      if (reply === undefined) throw new Error('no reply');
      return reply();
    });
    await save.click();
    await expect.poll(status).toBe('Config saved.');
    await save.click();
    await expect.element(screen.getByRole('alert', { name: 'Save errors' })).toBeVisible();
    expect(status()).toBeNull();
    await save.click();
    await expect.poll(status).toBe('Config saved.');
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });
});
