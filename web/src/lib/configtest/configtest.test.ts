import { expect, it } from 'vitest';
import { editedConfigKey } from '#lib/run/edited.ts';
import { configFixture, editedContext } from './configtest.svelte.ts';

it('returns a new saved config with no stored password on each call', () => {
  const config = configFixture();
  expect(config).toStrictEqual({
    debug: false,
    database: 'creator.db',
    chrome: { headless: true, timeout: '30s' },
    proxy: { direct: false },
    email: {
      imap_host: 'mail.example.com',
      imap_port: 993,
      imap_user: 'user@example.com',
      imap_pass_set: false,
      imap_domain: 'example.com',
    },
    account: { password_length: 16, enable_totp: true },
    concurrency: { max_workers: 10, max_browsers: 10, account_count: 10, account_timeout: '1m' },
  });
  expect(configFixture()).not.toBe(config);
});

it('gives a copy of the saved config under the one key, and the saved config keeps its values', () => {
  const saved = configFixture();
  const { config, context } = editedContext(saved);
  expect([...context.keys()]).toEqual([editedConfigKey]);
  expect(context.get(editedConfigKey)).toBe(config);
  expect(config).not.toBe(saved);
  expect(config).toEqual(saved);
  config.email.imap_host = 'imap.example.org';
  expect(saved.email.imap_host).toBe('mail.example.com');
});
