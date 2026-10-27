import type { components } from '#lib/api.gen.d.ts';

type Config = components['schemas']['Config'];

// Returns the count with its noun, for example "1 worker" or "4 workers".
function counted(count: number, noun: string): string {
  return `${String(count)} ${noun}${count === 1 ? '' : 's'}`;
}

function onOff(value: boolean): string {
  return value ? 'on' : 'off';
}

/** Returns the run settings as one line, for example "10 accounts, 4 workers, 2 browsers, 1m timeout, through proxies". */
export function runDigest({ concurrency, proxy }: Config): string {
  return [
    concurrency.account_count === -1
      ? 'No account limit'
      : counted(concurrency.account_count, 'account'),
    counted(concurrency.max_workers, 'worker'),
    // A max_browsers of 0 lets each worker run its own Chrome.
    concurrency.max_browsers === 0
      ? 'one browser per worker'
      : counted(concurrency.max_browsers, 'browser'),
    `${concurrency.account_timeout} timeout`,
    proxy.direct ? 'direct, no proxy' : 'through proxies',
  ].join(', ');
}

/**
 * Returns the mailbox as one line, for example "user@example.com at mail.example.com:993",
 * with ", OAuth2" after it for an OAuth2 login. It never holds a secret.
 */
export function mailboxDigest({ email }: Config): string {
  if (email.imap_user === '' || email.imap_host === '') {
    return 'Not set';
  }
  const mailbox = `${email.imap_user} at ${email.imap_host}:${String(email.imap_port)}`;
  return email.auth === 'oauth2' ? `${mailbox}, OAuth2` : mailbox;
}

/** Returns the account settings as one line, for example "16-character passwords, TOTP on". */
export function accountsDigest({ account }: Config): string {
  return `${String(account.password_length)}-character passwords, TOTP ${onOff(account.enable_totp)}`;
}

/** Returns the Chrome settings as one line, for example "Headless, 30s timeout". */
export function chromeDigest({ chrome }: Config): string {
  return `${chrome.headless ? 'Headless' : 'Headed'}, ${chrome.timeout} timeout`;
}

/** Returns the general settings as one line, for example "Debug off, creator.db". */
export function generalDigest({ debug, database }: Config): string {
  return `Debug ${onOff(debug)}, ${database}`;
}
