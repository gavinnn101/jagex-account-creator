import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { configEntries, failureText, runColumns, runDuration, runStatus } from './summary.ts';

type Run = components['schemas']['Run'];
type Config = components['schemas']['Config'];

// The key order of the server: the generated Go struct sorts the fields by name.
const config: Config = {
  account: { enable_totp: true, password_length: 16 },
  chrome: { headless: false, timeout: '30s' },
  concurrency: { account_count: -1, account_timeout: '1m0s', max_browsers: 0, max_workers: 4 },
  database: 'creator.db',
  debug: false,
  email: {
    imap_domain: 'example.com',
    imap_host: 'imap.example.com',
    imap_pass_set: true,
    imap_port: 993,
    imap_user: 'user',
  },
  proxy: { direct: true },
};

function run(over: Partial<Run> = {}): Run {
  return {
    id: 1,
    source: 'serve',
    started_at: '2026-10-22T10:00:00Z',
    config,
    counts: {
      attempts: 0,
      accounts: 0,
      unconfirmed: 0,
      failures: {},
      browser_bytes_sent: 0,
      browser_bytes_received: 0,
      http_bytes_sent: 0,
      http_bytes_received: 0,
    },
    ...over,
  };
}

describe('runDuration', () => {
  const now = new Date('2026-10-22T11:00:00Z');

  it.each([
    ['2026-10-22T10:01:05Z', '1:05'],
    ['2026-10-22T12:03:09Z', '2:03:09'],
    ['2026-10-22T10:00:00Z', '0:00'],
    // A host clock that steps back gives an end before the start.
    ['2026-10-22T09:58:58Z', '0:00'],
  ])('gives the time to an end of %s as %s', (ended, text) => {
    expect(runDuration(run({ ended_at: ended }), now)).toBe(text);
  });

  it('gives the time to now for a run with no end', () => {
    expect(runDuration(run(), now)).toBe('1:00:00');
  });
});

describe('runStatus', () => {
  it('marks a run with no end as in progress', () => {
    expect(runStatus(run())).toEqual({ tone: 'accent', label: 'In progress' });
  });

  it('marks a run that ended with an error, with the error as the title', () => {
    const failed = run({
      ended_at: '2026-10-22T10:01:00Z',
      error: 'checking the mailbox: refused',
    });
    expect(runStatus(failed)).toEqual({
      tone: 'danger',
      label: 'Error',
      title: 'checking the mailbox: refused',
    });
  });

  it('gives no badge for a clean end', () => {
    expect(runStatus(run({ ended_at: '2026-10-22T10:01:00Z' }))).toBeUndefined();
  });
});

describe('runColumns', () => {
  const clean = run({ ended_at: '2026-10-22T10:01:00Z' });

  it('hides each optional column when no run of the page has a value', () => {
    expect(runColumns([clean, clean])).toEqual({
      status: false,
      unconfirmed: false,
      failures: false,
    });
    expect(runColumns([])).toEqual({ status: false, unconfirmed: false, failures: false });
  });

  it('shows the status column for one run with a status', () => {
    expect(runColumns([clean, run()])).toEqual({
      status: true,
      unconfirmed: false,
      failures: false,
    });
  });

  it('shows the unconfirmed column for one run with an unconfirmed account', () => {
    const counts = { ...clean.counts, unconfirmed: 1 };
    expect(runColumns([clean, { ...clean, counts }])).toEqual({
      status: false,
      unconfirmed: true,
      failures: false,
    });
  });

  it('shows the failures column for one run with a failure', () => {
    const counts = { ...clean.counts, failures: { proxy: 1 } };
    expect(runColumns([{ ...clean, counts }, clean])).toEqual({
      status: false,
      unconfirmed: false,
      failures: true,
    });
  });
});

describe('failureText', () => {
  it('names each category with its count, in the order of the names', () => {
    const counts = { ...run().counts, failures: { proxy: 1, email: 2 } };
    expect(failureText(counts)).toBe('email 2, proxy 1');
  });

  it('is empty with no failure', () => {
    expect(failureText(run().counts)).toBe('');
  });
});

describe('configEntries', () => {
  it('gives one row for each value, with its dotted key, in the order of the object', () => {
    expect(configEntries(config)).toEqual([
      ['account.enable_totp', 'true'],
      ['account.password_length', '16'],
      ['chrome.headless', 'false'],
      ['chrome.timeout', '30s'],
      ['concurrency.account_count', '-1'],
      ['concurrency.account_timeout', '1m0s'],
      ['concurrency.max_browsers', '0'],
      ['concurrency.max_workers', '4'],
      ['database', 'creator.db'],
      ['debug', 'false'],
      ['email.imap_domain', 'example.com'],
      ['email.imap_host', 'imap.example.com'],
      ['email.imap_pass_set', 'true'],
      ['email.imap_port', '993'],
      ['email.imap_user', 'user'],
      ['proxy.direct', 'true'],
    ]);
  });

  it('keeps an empty value and gives no row for an absent key', () => {
    const email = {
      imap_domain: 'example.com',
      imap_host: 'imap.example.com',
      imap_port: 993,
      imap_user: '',
    };
    const rows = configEntries({ ...config, email });
    expect(rows.filter(([key]) => key.startsWith('email.'))).toEqual([
      ['email.imap_domain', 'example.com'],
      ['email.imap_host', 'imap.example.com'],
      ['email.imap_port', '993'],
      ['email.imap_user', ''],
    ]);
  });
});
