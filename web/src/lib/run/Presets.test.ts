import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { invalidate } from '$app/navigation';
import type { components } from '#lib/api.gen.d.ts';
import Presets from './Presets.svelte';

type Config = components['schemas']['Config'];
type Preset = components['schemas']['Preset'];

// The test page runs outside the SvelteKit router, so a real invalidate has no page to load.
vi.mock('$app/navigation', () => ({ invalidate: vi.fn(() => Promise.resolve()) }));

const config: Config = {
  debug: false,
  database: 'creator.db',
  chrome: { headless: true, timeout: '30s' },
  proxy: { direct: false },
  email: {
    imap_host: 'mail.example.com',
    imap_port: 993,
    imap_user: 'user@example.com',
    imap_pass: 'secret',
    imap_domain: 'example.com',
  },
  account: { password_length: 16, enable_totp: true },
  concurrency: { max_workers: 1, max_browsers: 1, account_count: 1, account_timeout: '1m' },
};

function preset(id: number, name: string, maxWorkers: number): Preset {
  return {
    id,
    name,
    saved_at: '2026-10-22T10:00:00Z',
    config: { ...config, concurrency: { ...config.concurrency, max_workers: maxWorkers } },
  };
}

const fast = preset(4, 'Fast', 8);
const slow = preset(2, 'Slow', 1);

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

async function renderPresets(presets: Preset[], ...replies: (() => Promise<Response>)[]) {
  const { sent, fetch } = fakeFetch(...replies);
  const onload = vi.fn<(c: Config) => void>();
  const screen = await render(Presets, { presets, config, onload, fetch });
  const button = (name: string) => screen.getByRole('button', { name, exact: true });
  const select = () => screen.getByRole('combobox', { name: 'Preset' });
  const nameField = () => screen.getByLabelText('Preset name');
  const status = () => screen.container.querySelector('[role="status"]')?.textContent ?? null;
  const alert = () => screen.container.querySelector('[role="alert"]');
  const errors = () =>
    screen
      .getByRole('alert', { name: 'Preset errors' })
      .getByRole('listitem')
      .elements()
      .map((li) => li.textContent);
  return { screen, sent, onload, button, select, nameField, status, alert, errors };
}

afterEach(() => {
  vi.mocked(invalidate).mockClear();
});

describe('Presets', () => {
  it('lists the presets and selects the first', async () => {
    const { screen, select, nameField, status, alert } = await renderPresets([fast, slow]);
    await expect.element(select()).toHaveValue('4');
    // The name is empty until a load or a typed name.
    expect(nameField().element()).toHaveValue('');
    const options = select()
      .getByRole('option')
      .elements()
      .map((o) => [o.getAttribute('value'), o.textContent]);
    expect(options).toEqual([
      ['4', 'Fast'],
      ['2', 'Slow'],
    ]);
    expect(screen.container.textContent).not.toContain('No preset yet.');
    expect(status()).toBeNull();
    expect(alert()).toBeNull();
  });

  it('says that no preset exists, and still offers the save', async () => {
    const { screen, button } = await renderPresets([]);
    await expect.element(screen.getByText('No preset yet.')).toBeVisible();
    expect(screen.getByRole('combobox').query()).toBeNull();
    expect(button('Load').query()).toBeNull();
    expect(screen.getByRole('button', { name: /delete/i }).query()).toBeNull();
    await expect.element(button('Save as preset')).toBeEnabled();
  });

  it('loads the selected preset with no request and takes its name', async () => {
    const { sent, onload, button, select, nameField, status } = await renderPresets([fast, slow]);
    await nameField().fill('typed');
    await select().selectOptions('Slow');
    await button('Load').click();
    expect(onload.mock.calls).toEqual([[slow.config]]);
    await expect.element(nameField()).toHaveValue('Slow');
    expect(sent).toEqual([]);
    expect(status()).toBeNull();
  });

  it('saves the config under the name, reads the list again and selects the saved preset', async () => {
    const { reply, answer } = held();
    const { screen, sent, button, select, nameField, status, alert } = await renderPresets(
      [fast, slow],
      reply,
    );
    await nameField().fill('New');
    await button('Save as preset').click();
    await expect.element(button('Save as preset')).toBeDisabled();
    const saved = preset(9, 'New', 1);
    answer(Response.json(saved));
    await expect.poll(status).toBe('Preset saved.');
    expect(button('Save as preset').element()).toBeEnabled();
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/presets');
    expect(sent).toEqual([
      { method: 'POST', path: '/api/presets', body: JSON.stringify({ name: 'New', config }) },
    ]);
    expect(alert()).toBeNull();
    await screen.rerender({ presets: [fast, saved, slow] });
    await expect.element(select()).toHaveValue('9');
  });

  it.for<[number, unknown, string[]]>([
    [
      422,
      { valid: false, errors: ['email.imap_host is required', 'database must be creator.db'] },
      ['email.imap_host is required', 'database must be creator.db'],
    ],
    [400, { message: 'name must not be empty' }, ['name must not be empty']],
  ])('shows the errors of a save with status %d', async ([code, body, want]) => {
    const { button, status, errors } = await renderPresets([fast], json(code, body));
    await button('Save as preset').click();
    await expect.poll(errors).toEqual(want);
    expect(status()).toBeNull();
    expect(invalidate).not.toHaveBeenCalled();
    await expect.element(button('Save as preset')).toBeEnabled();
  });

  it('shows a failed request, and the next change clears it', async () => {
    const { button, status, alert, errors } = await renderPresets(
      [fast],
      () => Promise.reject(new TypeError('Failed to fetch')),
      json(200, fast),
      json(400, { message: 'name must not be empty' }),
    );
    await button('Save as preset').click();
    await expect.poll(errors).toEqual(['TypeError: Failed to fetch']);
    await button('Save as preset').click();
    await expect.poll(status).toBe('Preset saved.');
    expect(alert()).toBeNull();
    await button('Save as preset').click();
    await expect.poll(errors).toEqual(['name must not be empty']);
    expect(status()).toBeNull();
  });

  it('deletes the selected preset only after the confirm step', async () => {
    const { sent, button, status } = await renderPresets([fast, slow], () =>
      Promise.resolve(new Response(null, { status: 204 })),
    );
    await button('Delete Fast').click();
    await expect.element(button('Confirm delete Fast')).toHaveTextContent('Confirm delete');
    expect(button('Delete Fast').query()).toBeNull();
    await button('Cancel delete Fast').click();
    await expect.element(button('Delete Fast')).toBeVisible();
    expect(button('Confirm delete Fast').query()).toBeNull();
    expect(sent).toEqual([]);

    await button('Delete Fast').click();
    await button('Confirm delete Fast').click();
    await expect.poll(status).toBe('Preset deleted.');
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/presets');
    expect(sent).toEqual([{ method: 'DELETE', path: '/api/presets/4', body: '' }]);
    await expect.element(button('Delete Fast')).toBeVisible();
  });

  it('deletes nothing on a double click on Delete', async () => {
    const { sent, button } = await renderPresets([fast, slow]);
    await button('Delete Fast').dblClick();
    await expect.element(button('Delete Fast')).toBeVisible();
    expect(button('Confirm delete Fast').query()).toBeNull();
    expect(sent).toEqual([]);
    expect(invalidate).not.toHaveBeenCalled();
  });

  it('shows the error of a refused delete', async () => {
    const { button, status, errors } = await renderPresets(
      [fast],
      json(404, { message: 'preset 4 not found' }),
    );
    await button('Delete Fast').click();
    await button('Confirm delete Fast').click();
    await expect.poll(errors).toEqual(['preset 4 not found']);
    expect(status()).toBeNull();
    expect(invalidate).not.toHaveBeenCalled();
    await expect.element(button('Delete Fast')).toBeVisible();
  });

  it('drops the confirm when the selection changes', async () => {
    const { button, select } = await renderPresets([fast, slow]);
    await button('Delete Fast').click();
    await expect.element(button('Confirm delete Fast')).toBeVisible();
    await select().selectOptions('Slow');
    await expect.element(button('Delete Slow')).toBeVisible();
    expect(button('Confirm delete Slow').query()).toBeNull();
    // A return to the first preset needs a new Delete click.
    await select().selectOptions('Fast');
    await expect.element(select()).toHaveValue('4');
    expect(button('Confirm delete Fast').query()).toBeNull();
    expect(button('Delete Fast').query()).not.toBeNull();
  });

  it('falls back to the first preset when the list loses the selected one', async () => {
    const { screen, onload, button, select } = await renderPresets([fast, slow]);
    await select().selectOptions('Slow');
    await screen.rerender({ presets: [fast] });
    await expect.element(select()).toHaveValue('4');
    await button('Load').click();
    expect(onload.mock.calls).toEqual([[fast.config]]);
  });
});
