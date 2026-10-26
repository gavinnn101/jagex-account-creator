import { describe, expect, it } from 'vitest';
import { render } from 'vitest-browser-svelte';
import type { components } from '#lib/api.gen.d.ts';
import { configFixture, editedContext } from '#lib/configtest/configtest.svelte.ts';
import ConfigFields from './ConfigFields.svelte';

type Config = components['schemas']['Config'];
type Screen = Awaited<ReturnType<typeof render>>;

function saved(passSet: boolean): Config {
  const config = configFixture();
  return { ...config, email: { ...config.email, imap_pass_set: passSet } };
}

// Answers each request with the next reply, and records the path and the body of each request.
function fakeFetch(...replies: Response[]) {
  const sent: [string, string][] = [];
  const fetch = async (input: RequestInfo | URL): Promise<Response> => {
    const request = new Request(input);
    sent.push([new URL(request.url).pathname, await request.text()]);
    const reply = replies.shift();
    if (reply === undefined) throw new Error('no reply');
    return reply;
  };
  return { sent, fetch };
}

// Renders the fields over a state copy of initial, as the page owns it, and returns that state.
async function renderFields(initial: Config, ...replies: Response[]) {
  const { config, context } = editedContext(initial);
  const { sent, fetch } = fakeFetch(...replies);
  const screen = await render(ConfigFields, { props: { fetch }, context });
  await expect.element(screen.getByText('Run settings')).toBeVisible();
  return { screen, config, sent };
}

function sections(screen: Screen): [string, boolean, string][] {
  return [...screen.container.querySelectorAll('details')].map((d) => {
    const spans = d.querySelectorAll('summary > span');
    return [spans[0]?.textContent.trim() ?? '', d.open, spans[1]?.textContent ?? ''];
  });
}

// A control in a closed section takes no input, so a test opens every section first.
async function openAll(screen: Screen): Promise<void> {
  for (const d of screen.container.querySelectorAll('details')) {
    if (!d.open) {
      d.querySelector('summary')?.click();
    }
  }
  await expect.poll(() => sections(screen).every(([, open]) => open)).toBe(true);
}

describe('ConfigFields', () => {
  it('refuses to render with no edited config above it', async () => {
    await expect(async () => render(ConfigFields, { fetch })).rejects.toThrow(
      'no edited config: render this component below provideEditedConfig',
    );
  });

  it('shows five sections, with only the run settings open, and a digest on each', async () => {
    const { screen } = await renderFields(saved(false));
    expect(sections(screen)).toEqual([
      ['Run settings', true, '10 accounts, 10 workers, 10 browsers, 1m timeout, through proxies'],
      ['Mailbox', false, 'user@example.com at mail.example.com:993'],
      ['Accounts', false, '16-character passwords, TOTP on'],
      ['Chrome', false, 'Headless, 30s timeout'],
      ['General', false, 'Debug off, creator.db'],
    ]);
    await expect.element(screen.getByLabelText('Max workers')).toBeVisible();
    await expect.element(screen.getByLabelText('IMAP host')).not.toBeVisible();
  });

  it('shows each key of the config', async () => {
    const { screen } = await renderFields(saved(false));
    const values: [string, string | number][] = [
      ['Account count', 10],
      ['Max workers', 10],
      ['Max browsers', 10],
      ['Account timeout', '1m'],
      ['IMAP host', 'mail.example.com'],
      ['IMAP port', 993],
      ['IMAP user', 'user@example.com'],
      ['IMAP password', ''],
      ['IMAP domains', 'example.com'],
      ['Password length', 16],
      ['Chrome timeout', '30s'],
      ['Database', 'creator.db'],
    ];
    for (const [label, value] of values) {
      await expect.element(screen.getByLabelText(label, { exact: true })).toHaveValue(value);
    }
    await expect.element(screen.getByLabelText('Debug')).not.toBeChecked();
    await expect.element(screen.getByLabelText('Direct, with no proxy')).not.toBeChecked();
    await expect.element(screen.getByLabelText('Headless')).toBeChecked();
    await expect.element(screen.getByLabelText('Enable TOTP')).toBeChecked();
    await expect.element(screen.getByLabelText('Database')).toHaveAttribute('readonly');
    await expect.element(screen.getByLabelText('IMAP password')).not.toHaveAttribute('placeholder');
  });

  it('describes the rules that a value alone does not show', async () => {
    const { screen } = await renderFields(saved(false));
    const descriptions: [string, string][] = [
      ['Account count', '-1: no limit.'],
      ['IMAP domains', 'One domain per line. Each account takes one at random.'],
      [
        'Account timeout',
        'A Go duration, for example 1m. It bounds each attempt up to the password POST.',
      ],
      ['Direct, with no proxy', 'A run with no enabled proxy fails unless this is on.'],
      ['Chrome timeout', 'A Go duration, for example 1m30s.'],
      ['Database', 'Serve opens this database. A run must use it.'],
    ];
    for (const [label, text] of descriptions) {
      await expect
        .element(screen.getByLabelText(label, { exact: true }))
        .toHaveAccessibleDescription(text);
    }
    // The two duration hints are titles, so they take no line below the field.
    for (const label of ['Account timeout', 'Chrome timeout']) {
      expect(screen.getByLabelText(label).element()).not.toHaveAttribute('aria-describedby');
    }
  });

  it('edits the config and each digest follows', async () => {
    const initial = saved(false);
    const { screen, config } = await renderFields(initial);
    await openAll(screen);
    const edits: [string, string][] = [
      ['Account count', '-1'],
      ['Max workers', '3'],
      ['Max browsers', '0'],
      ['Account timeout', '2m'],
      ['IMAP host', 'imap.example.org'],
      ['IMAP port', '143'],
      ['IMAP user', 'other@example.org'],
      ['IMAP domains', 'example.org'],
      ['Password length', '20'],
      ['Chrome timeout', '45s'],
    ];
    for (const [label, value] of edits) {
      await screen.getByLabelText(label, { exact: true }).fill(value);
    }
    for (const label of ['Debug', 'Direct, with no proxy', 'Headless', 'Enable TOTP']) {
      await screen.getByLabelText(label).click();
    }
    expect(config).toEqual({
      debug: true,
      database: 'creator.db',
      chrome: { headless: false, timeout: '45s' },
      proxy: { direct: true },
      email: {
        imap_host: 'imap.example.org',
        imap_port: 143,
        imap_user: 'other@example.org',
        imap_pass_set: false,
        imap_domains: ['example.org'],
      },
      account: { password_length: 20, enable_totp: false },
      concurrency: { max_workers: 3, max_browsers: 0, account_count: -1, account_timeout: '2m' },
    });
    expect(sections(screen).map(([, , digest]) => digest)).toEqual([
      'No account limit, 3 workers, one browser per worker, 2m timeout, direct, no proxy',
      'other@example.org at imap.example.org:143',
      '20-character passwords, TOTP off',
      'Headed, 45s timeout',
      'Debug on, creator.db',
    ]);
    expect(initial).toEqual(saved(false));
  });

  it('holds 0 for a cleared number field, never null', async () => {
    const { screen, config } = await renderFields(saved(false));
    await openAll(screen);
    const numbers = [
      'IMAP port',
      'Password length',
      'Account count',
      'Max workers',
      'Max browsers',
    ];
    for (const label of numbers) {
      await screen.getByLabelText(label, { exact: true }).fill('');
    }
    expect(config).toMatchObject({
      email: { imap_port: 0 },
      account: { password_length: 0 },
      concurrency: { max_workers: 0, max_browsers: 0, account_count: 0 },
    });
  });

  it('sends a typed password and no password for an empty field', async () => {
    const { screen, config } = await renderFields(saved(true));
    await openAll(screen);
    const pass = screen.getByLabelText('IMAP password');
    await expect.element(pass).toHaveAttribute('placeholder', 'Stored. Empty keeps it.');
    await expect.element(pass).toHaveAttribute('type', 'password');
    expect('imap_pass' in config.email).toBe(false);
    await pass.fill('secret');
    await expect.element(pass).toHaveValue('secret');
    expect(config.email.imap_pass).toBe('secret');
    await pass.fill('');
    await expect.element(pass).toHaveValue('');
    expect('imap_pass' in config.email).toBe(false);
  });

  it('sets the domains from the typed lines', async () => {
    const { screen, config } = await renderFields(saved(false));
    await openAll(screen);
    const domains = screen.getByLabelText('IMAP domains');
    await expect.element(domains).toHaveAttribute('spellcheck', 'false');
    await domains.fill('a.example\nb.example');
    await expect.element(domains).toHaveValue('a.example\nb.example');
    expect(config.email.imap_domains).toEqual(['a.example', 'b.example']);
  });

  it('drops a blank line and the spaces around a domain', async () => {
    const { screen, config } = await renderFields(saved(false));
    await openAll(screen);
    await screen.getByLabelText('IMAP domains').fill(' a.example \n\n   \n\tb.example  ');
    expect(config.email.imap_domains).toEqual(['a.example', 'b.example']);
  });

  it('keeps a typed trailing newline in the text', async () => {
    const { screen, config } = await renderFields(saved(false));
    await openAll(screen);
    const domains = screen.getByLabelText('IMAP domains');
    await domains.fill('a.example\nb.example\n');
    await expect.element(domains).toHaveValue('a.example\nb.example\n');
    expect(config.email.imap_domains).toEqual(['a.example', 'b.example']);
  });

  it('shows a list that changes from outside, as a preset Load does', async () => {
    const { screen, config } = await renderFields(saved(false));
    await openAll(screen);
    const domains = screen.getByLabelText('IMAP domains');
    await domains.fill('a.example\n');
    config.email.imap_domains = ['c.example', 'd.example'];
    await expect.element(domains).toHaveValue('c.example\nd.example');
  });

  it('checks the mailbox with the edited email section', async () => {
    const { screen, sent } = await renderFields(saved(false), Response.json({ ok: true }));
    await openAll(screen);
    await screen.getByLabelText('IMAP host').fill('imap.example.org');
    const mailbox = screen.container.querySelectorAll('details')[1];
    const check = screen.getByRole('button', { name: 'Check mailbox' });
    expect(mailbox?.contains(check.element())).toBe(true);
    await check.click();
    await expect.element(screen.getByRole('status')).toHaveTextContent('Login OK.');
    expect(sent).toEqual([
      [
        '/api/checks/mailbox',
        JSON.stringify({ ...saved(false).email, imap_host: 'imap.example.org' }),
      ],
    ]);
  });
});
