import { describe, expect, it } from 'vitest';
import type { components } from '#lib/api.gen.d.ts';
import { configFixture } from '#lib/configtest/configtest.svelte.ts';
import { accountsDigest, chromeDigest, generalDigest, mailboxDigest, runDigest } from './digest.ts';

type Config = components['schemas']['Config'];

const fixture = configFixture();

// A typed password, so the last test can check that no digest holds it.
const config: Config = {
  ...fixture,
  email: { ...fixture.email, imap_pass: 'secret-pass', imap_pass_set: true },
  concurrency: { ...fixture.concurrency, max_workers: 4, max_browsers: 2 },
};

function withConcurrency(values: Partial<Config['concurrency']>): Config {
  return { ...config, concurrency: { ...config.concurrency, ...values } };
}

function withEmail(values: Partial<Config['email']>): Config {
  return { ...config, email: { ...config.email, ...values } };
}

const digests = [runDigest, mailboxDigest, accountsDigest, chromeDigest, generalDigest];

describe('runDigest', () => {
  it('counts the accounts, the workers and the browsers, with the timeout and the route', () => {
    expect(runDigest(config)).toBe(
      '10 accounts, 4 workers, 2 browsers, 1m timeout, through proxies',
    );
  });

  it('says one of each in the singular', () => {
    expect(runDigest(withConcurrency({ account_count: 1, max_workers: 1, max_browsers: 1 }))).toBe(
      '1 account, 1 worker, 1 browser, 1m timeout, through proxies',
    );
  });

  it('names the unlimited count, the browser default and a direct run', () => {
    const direct = {
      ...withConcurrency({ account_count: -1, max_browsers: 0, account_timeout: '1m30s' }),
      proxy: { direct: true },
    };
    expect(runDigest(direct)).toBe(
      'No account limit, 4 workers, one browser per worker, 1m30s timeout, direct, no proxy',
    );
  });

  it('counts zero accounts as a count, not as no limit', () => {
    expect(runDigest(withConcurrency({ account_count: 0 }))).toMatch(/^0 accounts, /);
  });
});

describe('mailboxDigest', () => {
  it('names the user, the host and the port', () => {
    expect(mailboxDigest(config)).toBe('user@example.com at mail.example.com:993');
  });

  it('marks an OAuth2 login with no secret in the text', () => {
    const oauth2 = withEmail({
      auth: 'oauth2',
      oauth2: {
        client_id: 'client',
        client_secret: 'secret-client',
        refresh_token: 'secret-refresh',
        token_url: '',
      },
    });
    expect(mailboxDigest(oauth2)).toBe('user@example.com at mail.example.com:993, OAuth2');
  });

  it.for<[string, Partial<Config['email']>]>([
    ['no user', { imap_user: '' }],
    ['no host', { imap_host: '' }],
    ['neither', { imap_user: '', imap_host: '' }],
  ])('says "Not set" with %s', ([, values]) => {
    expect(mailboxDigest(withEmail(values))).toBe('Not set');
  });
});

describe('accountsDigest', () => {
  it('gives the password length and the TOTP state', () => {
    expect(accountsDigest(config)).toBe('16-character passwords, TOTP on');
    expect(
      accountsDigest({ ...config, account: { password_length: 24, enable_totp: false } }),
    ).toBe('24-character passwords, TOTP off');
  });
});

describe('chromeDigest', () => {
  it('gives the mode and the timeout', () => {
    expect(chromeDigest(config)).toBe('Headless, 30s timeout');
    expect(chromeDigest({ ...config, chrome: { headless: false, timeout: '1m' } })).toBe(
      'Headed, 1m timeout',
    );
  });
});

describe('generalDigest', () => {
  it('gives the debug state and the database', () => {
    expect(generalDigest(config)).toBe('Debug off, creator.db');
    expect(generalDigest({ ...config, debug: true, database: 'other.db' })).toBe(
      'Debug on, other.db',
    );
  });
});

it('never puts the IMAP password in a digest', () => {
  for (const digest of digests) {
    expect(digest(config)).not.toContain('secret-pass');
  }
});
