import { describe, expect, it } from 'vitest';
import { parseMailboxLine, parseMailboxList } from './mailboxLine.ts';

const user = 'someone@outlook.com';
const clientId = '9e5f94bc-e8a4-4e73-b8be-63364c29d753';
// The shape of a Microsoft refresh token: long, with ! * $ and no separator.
const token = `M.C552_BAY.0.U.-Ck${'a1B2*c3!D4$'.repeat(20)}`;
const want = { user, clientId, refreshToken: token };

describe('parseMailboxLine', () => {
  it.for<[string, string]>([
    ['colons', `${user}:hunter2:${token}:${clientId}`],
    ['bars', `${user}|hunter2|${token}|${clientId}`],
    ['dashes', `${user}----hunter2----${token}----${clientId}`],
    ['the client ID before the token', `${user}:hunter2:${clientId}:${token}`],
    ['no password', `${user}|${token}|${clientId}`],
    ['spaces around the parts and the line', `  ${user} | hunter2 | ${token} | ${clientId}  \n`],
    [
      'a password with colons and bars split by dashes',
      `${user}----p:a|ss----${token}----${clientId}`,
    ],
    ['a password with colons split by colons', `${user}:p:a:ss:${token}:${clientId}`],
    ['an upper-case client ID', `${user}:hunter2:${token}:${clientId.toUpperCase()}`],
  ])('reads a line with %s', ([, line]) => {
    const clientIdOf = line.includes(clientId) ? clientId : clientId.toUpperCase();
    expect(parseMailboxLine(line)).toEqual({ ...want, clientId: clientIdOf });
  });

  it.for<[string, string, string]>([
    ['an empty line', '  ', 'Paste a line first.'],
    ['no email address', `hunter2:${token}:${clientId}`, 'The line holds no email address.'],
    [
      'an @ with no domain dot',
      `someone@outlook:${token}:${clientId}`,
      'The line holds no email address.',
    ],
    [
      'no client ID',
      `${user}:hunter2:${token}`,
      'The line holds no client ID, a GUID such as 00000000-0000-0000-0000-000000000000.',
    ],
    ['no refresh token', `${user}:hunter2:${clientId}`, 'The line holds no refresh token.'],
    [
      'a refresh token one character short',
      `${user}:${'x'.repeat(99)}:${clientId}`,
      'The line holds no refresh token.',
    ],
  ])('refuses %s', ([, line, error]) => {
    expect(parseMailboxLine(line)).toEqual({ error });
  });

  it('takes a token of the minimum length', () => {
    const short = 'x'.repeat(100);
    expect(parseMailboxLine(`${user}:${short}:${clientId}`)).toEqual({
      ...want,
      refreshToken: short,
    });
  });
});

describe('parseMailboxList', () => {
  it('reads each line, skips the empty ones, and numbers each line that does not read', () => {
    const text = [
      `${user}:${token}:${clientId}`,
      '',
      '  ',
      'junk',
      `b@outlook.com|${token}|${clientId}`,
    ].join('\r\n');
    expect(parseMailboxList(text)).toEqual({
      mailboxes: [want, { ...want, user: 'b@outlook.com' }],
      errors: [{ line: 4, error: 'The line holds no email address.' }],
    });
  });

  it('keeps the first line of a mailbox, in any letter case', () => {
    const text = [
      `${user}:${token}:${clientId}`,
      `${user.toUpperCase()}:${token}x:${clientId}`,
    ].join('\n');
    expect(parseMailboxList(text)).toEqual({ mailboxes: [want], errors: [] });
  });

  it('gives an empty list for empty text', () => {
    expect(parseMailboxList('')).toEqual({ mailboxes: [], errors: [] });
  });
});
