import { beforeEach, describe, expect, it } from 'vitest';
import { configFixture } from '#lib/configtest/configtest.svelte.ts';
import { pasted, removeMailboxes } from './pasted.svelte.ts';

const clientId = '9e5f94bc-e8a4-4e73-b8be-63364c29d753';
const token = (user: string) => `M.C552_BAY.0.U.-Ck${user}${'a1B2*c3!D4$'.repeat(20)}`;
const line = (user: string) => `${user}:hunter2:${token(user)}:${clientId}`;
const listed = (user: string, refreshToken?: string) => ({
  imap_host: 'outlook.office365.com',
  imap_port: 993,
  imap_user: user,
  imap_domains: [],
  auth: 'oauth2' as const,
  oauth2:
    refreshToken === undefined
      ? { client_id: clientId, token_url: 'https://login.example.com/token' }
      : {
          client_id: clientId,
          refresh_token: refreshToken,
          token_url: 'https://login.example.com/token',
        },
});

describe('removeMailboxes', () => {
  beforeEach(() => {
    pasted.text = '';
    pasted.picked = '';
  });

  it('drops the mailboxes and their pasted lines, and keeps the other lines', () => {
    pasted.text = [
      line('a@outlook.com'),
      '',
      'junk',
      line('b@outlook.com'),
      line('c@outlook.com'),
    ].join('\n');
    const { email } = configFixture();
    email.imap_user = 'c@outlook.com';
    email.mailboxes = [listed('a@outlook.com'), listed('b@outlook.com'), listed('c@outlook.com')];

    removeMailboxes(email, ['B@Outlook.com']);

    expect(pasted.text).toBe([line('a@outlook.com'), '', 'junk', line('c@outlook.com')].join('\n'));
    expect(email.mailboxes.map((m) => m.imap_user)).toEqual(['a@outlook.com', 'c@outlook.com']);
    expect(email.imap_user).toBe('c@outlook.com');
    expect(pasted.picked).toBe('');
  });

  it('fills the login fields from the first mailbox left when they held a removed one', () => {
    const { email } = configFixture();
    email.imap_user = 'a@outlook.com';
    email.oauth2.refresh_token = 'old';
    email.mailboxes = [
      listed('a@outlook.com'),
      listed('b@outlook.com', 'token-b'),
      listed('c@outlook.com'),
    ];

    removeMailboxes(email, ['a@outlook.com']);

    expect(email.imap_user).toBe('b@outlook.com');
    expect(email.oauth2.client_id).toBe(clientId);
    expect(email.oauth2.refresh_token).toBe('token-b');
    expect(pasted.picked).toBe('b@outlook.com');

    removeMailboxes(email, ['b@outlook.com']);
    expect(email.imap_user).toBe('c@outlook.com');
    expect(email.oauth2).not.toHaveProperty('refresh_token');
  });

  it('drops the list when no mailbox is left, and keeps the login fields', () => {
    const { email } = configFixture();
    email.imap_user = 'a@outlook.com';
    email.mailboxes = [listed('a@outlook.com')];

    removeMailboxes(email, ['a@outlook.com']);

    expect(email).not.toHaveProperty('mailboxes');
    expect(email.imap_user).toBe('a@outlook.com');
  });

  it('changes nothing in an email section with no list', () => {
    const { email } = configFixture();
    const before = JSON.stringify(email);
    removeMailboxes(email, ['user@example.com']);
    expect(JSON.stringify(email)).toBe(before);
  });
});
