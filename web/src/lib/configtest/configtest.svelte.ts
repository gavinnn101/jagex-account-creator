// The test helpers of the edited config of the run screen. Only test files import this module.
import { SvelteMap } from 'svelte/reactivity';
import type { components } from '#lib/api.gen.d.ts';
import { editedConfigKey } from '#lib/run/edited.ts';

type Config = components['schemas']['Config'];

/** Returns a saved config of serve with no stored password. Each call returns a new object. */
export function configFixture(): Config {
  return {
    debug: false,
    database: 'creator.db',
    chrome: { headless: true, timeout: '30s' },
    proxy: { direct: false },
    email: {
      imap_host: 'mail.example.com',
      imap_port: 993,
      imap_user: 'user@example.com',
      imap_pass_set: false,
      imap_domains: ['example.com'],
    },
    account: { password_length: 16, enable_totp: true },
    concurrency: { max_workers: 10, max_browsers: 10, account_count: 10, account_timeout: '1m' },
  };
}

/**
 * Returns a state copy of saved, as the run screen owns it, and the context that gives it
 * to a component under test. The test reads and edits the copy, and saved keeps its values.
 */
export function editedContext(saved: Config): { config: Config; context: Map<symbol, Config> } {
  const config = $state(structuredClone(saved));
  return { config, context: new SvelteMap([[editedConfigKey, config]]) };
}
