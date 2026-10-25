import { describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import { invalidate } from '$app/navigation';
import type { components } from '#lib/api.gen.d.ts';
import { configFixture, editedContext } from '#lib/configtest/configtest.svelte.ts';
import RunControls from './RunControls.svelte';

// The test page runs outside the SvelteKit router, so a real invalidate has no page to load.
vi.mock('$app/navigation', () => ({ invalidate: vi.fn(() => Promise.resolve()) }));

type Phase = components['schemas']['RunState']['phase'];
type Preset = components['schemas']['Preset'];

const initial = configFixture();

// A preset whose every value differs from initial.
const fast: Preset = {
  id: 7,
  name: 'Fast',
  saved_at: '2026-10-22T10:00:00Z',
  config: {
    debug: true,
    database: 'creator.db',
    chrome: { headless: false, timeout: '45s' },
    proxy: { direct: true },
    email: {
      imap_host: 'imap.fast.example',
      imap_port: 143,
      imap_user: 'fast@example.com',
      imap_pass_set: false,
      imap_domain: 'fast.example',
    },
    account: { password_length: 20, enable_totp: false },
    concurrency: { max_workers: 8, max_browsers: 4, account_count: -1, account_timeout: '2m' },
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

// Renders the controls over a state copy of initial, as the page owns it.
async function renderControls(phase: Phase | undefined, ...replies: (() => Promise<Response>)[]) {
  const { config, context } = editedContext(initial);
  const { sent, fetch } = fakeFetch(...replies);
  const screen = await render(RunControls, {
    props: { presets: [fast], phase, error: undefined, fetch },
    context,
  });
  const button = (name: string) => screen.getByRole('button', { name, exact: true });
  await expect.element(button('Start')).toBeInTheDocument();
  return { screen, config, sent, button };
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

const running = { phase: 'running', workers: [], cooldowns: [] };

describe('RunControls', () => {
  it.for<[Phase | undefined, boolean, boolean, boolean]>([
    [undefined, false, false, false],
    ['idle', true, false, false],
    ['running', false, true, true],
    ['stopping', false, false, true],
    ['stopping_now', false, false, false],
    ['ended', true, false, false],
  ])('turns on the buttons of phase %s', async ([phase, start, stop, stopNow]) => {
    const { screen, button } = await renderControls(phase);
    const states = ['Start', 'Stop', 'Stop now'].map(
      (name) => !(button(name).element() as HTMLButtonElement).disabled,
    );
    expect(states).toEqual([start, stop, stopNow]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('ranks the run buttons and names the stop stages', async () => {
    const { button } = await renderControls('running');
    const variants = ['Start', 'Stop', 'Stop now'].map((name) =>
      button(name).element().getAttribute('data-variant'),
    );
    expect(variants).toEqual(['primary', 'secondary', 'danger']);
    for (const name of ['Stop', 'Stop now']) {
      await expect
        .element(button(name))
        .toHaveAccessibleDescription(
          'Stop: no new attempt starts, and an attempt that sent its password finishes. Stop now: it also ends that work and kills Chrome.',
        );
    }
  });

  it('starts a run with the edited config, and Start is off while the request runs', async () => {
    const { reply, answer } = held();
    const { screen, config, sent, button } = await renderControls('idle', reply);
    config.concurrency.max_workers = 4;
    await button('Start').click();
    await expect.element(button('Start')).toBeDisabled();
    answer(Response.json(running, { status: 202 }));
    await expect.element(button('Start')).toBeEnabled();
    expect(sent).toEqual([
      {
        method: 'POST',
        path: '/api/run',
        body: JSON.stringify({
          ...initial,
          concurrency: { ...initial.concurrency, max_workers: 4 },
        }),
      },
    ]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('shows each message of a refused config', async () => {
    const errors = ['email.imap_host must be set', 'concurrency.max_workers must be at least 1'];
    const { screen, button } = await renderControls('idle', json(422, { valid: false, errors }));
    await button('Start').click();
    const alert = screen.getByRole('alert', { name: 'Request errors' });
    await expect.element(alert).toBeVisible();
    expect(
      alert
        .getByRole('listitem')
        .elements()
        .map((li) => li.textContent),
    ).toEqual(errors);
  });

  it.for([
    [400, 'chrome.timeout: time: invalid duration "x"'],
    [409, 'a run is in progress'],
  ])('shows the message of a %d answer', async ([status, message]) => {
    const { screen, button } = await renderControls('idle', json(Number(status), { message }));
    await button('Start').click();
    await expect
      .element(screen.getByRole('alert', { name: 'Request errors' }))
      .toHaveTextContent(String(message));
  });

  it('shows a failed request', async () => {
    const { screen, button } = await renderControls('idle', () =>
      Promise.reject(new TypeError('Failed to fetch')),
    );
    await button('Start').click();
    await expect
      .element(screen.getByRole('alert', { name: 'Request errors' }))
      .toHaveTextContent('TypeError: Failed to fetch');
    await expect.element(button('Start')).toBeEnabled();
  });

  it('sends each stop stage and clears the last errors', async () => {
    const { screen, sent, button } = await renderControls(
      'idle',
      json(409, { message: 'serve is stopping' }),
      json(200, running),
      json(200, running),
    );
    await button('Start').click();
    await expect.element(screen.getByRole('alert')).toHaveTextContent('serve is stopping');
    await screen.rerender({ phase: 'running' });
    await button('Stop').click();
    await expect.poll(() => screen.container.querySelector('[role="alert"]')).toBeNull();
    await button('Stop now').click();
    await expect.poll(() => sent.length).toBe(3);
    expect(sent.slice(1).map((s) => [s.method, s.path, s.body])).toEqual([
      ['POST', '/api/run/stop', ''],
      ['POST', '/api/run/stop-now', ''],
    ]);
  });

  it('shows the error of the run under the stop stages', async () => {
    const { screen } = await renderControls('ended');
    expect(screen.getByRole('alert', { name: 'Run error' }).query()).toBeNull();
    await screen.rerender({ error: 'imap login: EOF' });
    const alert = screen.getByRole('alert', { name: 'Run error' });
    await expect.element(alert).toHaveTextContent('Run error: imap login: EOF');
    const stages = screen.getByText(/^Stop: no new attempt starts/).element();
    expect(stages.compareDocumentPosition(alert.element())).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
  });

  it('fills the config from a loaded preset and clears a typed password', async () => {
    const { config, sent, button } = await renderControls('idle', json(202, running));
    config.email.imap_pass = 'secret';
    config.concurrency.max_workers = 3;
    await button('Load').click();
    await expect.poll(() => config.concurrency.max_workers).toBe(8);
    expect(config).toEqual(fast.config);
    await button('Start').click();
    await expect.poll(() => sent.length).toBe(1);
    expect(sent[0]?.body).toBe(JSON.stringify(fast.config));
    // The config is a copy, so the preset of the list keeps its values.
    config.concurrency.max_workers = 2;
    expect(fast.config.concurrency.max_workers).toBe(8);
  });

  it('saves the edited config as a preset', async () => {
    const { screen, config, sent, button } = await renderControls('idle', json(200, fast));
    config.concurrency.max_workers = 5;
    await screen.getByLabelText('Preset name').fill('Five');
    await button('Save as preset').click();
    await expect.poll(() => sent.length).toBe(1);
    expect(sent[0]).toEqual({
      method: 'POST',
      path: '/api/presets',
      body: JSON.stringify({
        name: 'Five',
        config: { ...initial, concurrency: { ...initial.concurrency, max_workers: 5 } },
      }),
    });
    await expect.element(screen.getByRole('status')).toHaveTextContent('Preset saved.');
    expect(invalidate).toHaveBeenCalledExactlyOnceWith('/api/presets');
  });
});
