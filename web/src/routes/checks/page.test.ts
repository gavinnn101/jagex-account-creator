import { isHttpError } from '@sveltejs/kit';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import { configFixture } from '#lib/configtest/configtest.svelte.ts';
import Page from './+page.svelte';
import { load } from './+page';
import type { PageLoad } from './$types';

type ChromeCheck = components['schemas']['ChromeCheck'];
type Config = components['schemas']['Config'];

const found: ChromeCheck = { ok: true, version: '155.0.1.2', shape_major: 155, mismatch: false };

afterEach(() => {
  vi.unstubAllGlobals();
});

// Answers each GET with the reply for its path, and records each request.
function loadWith(replies: Record<string, Response>) {
  const requests: Request[] = [];
  const fetch = (input: RequestInfo | URL): Promise<Response> => {
    const request = new Request(input);
    requests.push(request);
    const reply = replies[new URL(request.url).pathname];
    if (reply === undefined) throw new Error('no reply');
    return Promise.resolve(reply);
  };
  const event = { fetch } as Parameters<PageLoad>[0];
  return { requests, result: load(event) };
}

async function loadError(result: ReturnType<typeof load>): Promise<unknown> {
  return Promise.resolve(result).catch((e: unknown) => e);
}

interface Posted {
  method: string;
  path: string;
  body: unknown;
}

// Answers each request with the next reply, and records each request with its JSON body.
function stubFetch(...replies: Response[]): Posted[] {
  const posted: Posted[] = [];
  vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
    const request = new Request(input);
    posted.push({
      method: request.method,
      path: new URL(request.url).pathname,
      body: await request.json(),
    });
    const reply = replies.shift();
    if (reply === undefined) throw new Error('no reply');
    return reply;
  });
  return posted;
}

function renderPage(check: ChromeCheck = found, config: Config = configFixture()) {
  return render(Page, { data: { check, config }, params: {} });
}

type Screen = Awaited<ReturnType<typeof render>>;

// The markup wraps the text over several lines, so the text holds runs of white space.
function text(element: Element | null): string {
  return (element?.textContent ?? '').replace(/\s+/g, ' ').trim();
}

// The term and the value of each row of the list of a section, by the heading of the section.
function rows(screen: Screen, section: string): string[][] {
  const region = screen.getByRole('region', { name: section, exact: true }).element();
  return [...region.querySelectorAll('dt')].map((dt) => [text(dt), text(dt.nextElementSibling)]);
}

// The text and the tone of each badge of the Chrome section.
function badges(screen: Screen): string[][] {
  const region = screen.getByRole('region', { name: 'Chrome', exact: true }).element();
  return [...region.querySelectorAll('[data-tone]')].map((b) => [
    text(b),
    b.getAttribute('data-tone') ?? '',
  ]);
}

function mailboxButton(screen: Screen) {
  return screen.getByRole('button', { name: 'Check mailbox', exact: true });
}

describe('load', () => {
  it('reads the Chrome check and the config of the server', async () => {
    const config = configFixture();
    const { requests, result } = loadWith({
      '/api/checks/chrome': Response.json(found),
      '/api/config': Response.json(config),
    });
    await expect(result).resolves.toEqual({ check: found, config });
    expect(requests.map((r) => [r.method, new URL(r.url).pathname])).toEqual([
      ['GET', '/api/checks/chrome'],
      ['GET', '/api/config'],
    ]);
  });

  it('fails with the status of a refused check', async () => {
    const { result } = loadWith({
      '/api/checks/chrome': Response.json({ message: 'boom' }, { status: 500 }),
      '/api/config': Response.json(configFixture()),
    });
    const err = await loadError(result);
    expect(isHttpError(err, 500)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Chrome check failed with HTTP 500' } });
  });

  it('fails with the status of a refused config read', async () => {
    const { result } = loadWith({
      '/api/checks/chrome': Response.json(found),
      '/api/config': Response.json({ message: 'boom' }, { status: 503 }),
    });
    const err = await loadError(result);
    expect(isHttpError(err, 503)).toBe(true);
    expect(err).toMatchObject({ body: { message: 'Config read failed with HTTP 503' } });
  });
});

describe('Chrome', () => {
  it('shows a found Chrome whose version matches the request shapes', async () => {
    const screen = await renderPage();
    await expect.element(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Checks');
    expect(rows(screen, 'Chrome')).toEqual([
      ['Status', 'Found'],
      ['Version', '155.0.1.2 Matches'],
      ['Request shape major', '155'],
    ]);
    expect(badges(screen)).toEqual([
      ['Found', 'success'],
      ['Matches', 'success'],
    ]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });

  it('warns of a version that differs from the request shapes', async () => {
    const check = { ok: true, version: '148.0.7778.5', shape_major: 155, mismatch: true };
    const screen = await renderPage(check);
    const alert = screen.getByRole('alert');
    await expect.element(alert).toHaveClass('text-warning');
    expect(rows(screen, 'Chrome')).toContainEqual(['Version', '148.0.7778.5 Differs']);
    expect(badges(screen)).toEqual([
      ['Found', 'success'],
      ['Differs', 'warning'],
    ]);
    expect(text(alert.element())).toBe(
      'The installed Chrome major differs from Chrome 155, the Chrome that the request shapes copy. ' +
        'The browser and the HTTP requests then show two different Chrome versions.',
    );
    expect(alert.element().querySelector('svg[aria-hidden="true"]')).not.toBeNull();
  });

  it('shows the error of a missing Chrome', async () => {
    const check = { ok: false, error: 'chrome not found', shape_major: 155 };
    const screen = await renderPage(check);
    await expect.element(screen.getByText('Not found', { exact: true })).toBeVisible();
    expect(rows(screen, 'Chrome')).toEqual([
      ['Status', 'Not found'],
      ['Error', 'chrome not found'],
      ['Request shape major', '155'],
    ]);
    expect(badges(screen)).toEqual([['Not found', 'danger']]);
    expect(screen.container.querySelector('[role="alert"]')).toBeNull();
  });
});

describe('Mailbox', () => {
  it('checks the saved mailbox with the stored password', async () => {
    const config = configFixture();
    const posted = stubFetch(
      Response.json({ ok: true }),
      Response.json({ message: 'request body has an error' }, { status: 400 }),
    );
    const screen = await renderPage(found, config);
    expect(rows(screen, 'Mailbox')).toEqual([
      ['Saved mailbox', 'user@example.com at mail.example.com:993'],
    ]);
    expect(screen.getByRole('link', { name: 'run screen' }).query()).toBeNull();

    const button = mailboxButton(screen);
    await expect.element(button).toBeEnabled();
    await button.click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Login OK.');
    // No imap_pass in the body, so serve logs in with the password that it stores.
    expect(posted).toEqual([{ method: 'POST', path: '/api/checks/mailbox', body: config.email }]);
    expect(posted[0]?.body).not.toHaveProperty('imap_pass');

    await button.click();
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('Mailbox check failed: request body has an error');
    expect(posted).toHaveLength(2);
  });

  it('turns the check off for a saved config with no mailbox, and says why', async () => {
    const config = configFixture();
    config.email.imap_host = '';
    const screen = await renderPage(found, config);
    await expect.element(mailboxButton(screen)).toBeDisabled();
    expect(rows(screen, 'Mailbox')).toEqual([['Saved mailbox', 'Not set']]);
    const link = screen.getByRole('link', { name: 'run screen' });
    await expect.element(link).toHaveAttribute('href', '/');
    expect(text(link.element().parentElement)).toBe(
      'The saved config has no mailbox. Set it on the run screen and save.',
    );
  });
});
