/** The OAuth2 login of one mailbox, read from a pasted line. */
export interface MailboxLine {
  user: string;
  clientId: string;
  refreshToken: string;
}

/** The IMAP host and the token endpoint of Outlook, which a fill sets where the section has none. */
export const outlookIMAPHost = 'outlook.office365.com';
export const outlookTokenURL = 'https://login.microsoftonline.com/common/oauth2/v2.0/token';

// The first separator that the line holds splits it. ---- comes first, because a password can hold - or :.
const separators = ['----', '|', ':'];
const emailRe = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const clientIdRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
// A refresh token is far longer than a password. Microsoft's run to several hundred characters.
const minTokenLength = 100;

/**
 * Reads the email, the client ID and the refresh token of a line such as
 * email:password:refresh_token:client_id. The parts can come in any order, and
 * ----, | or : can separate them. A part is told by its shape: the email has an @,
 * the client ID is a GUID, and the refresh token is the longest other part. The
 * other parts, such as the password, are dropped. It returns the error text of a
 * line that lacks a part.
 */
export function parseMailboxLine(text: string): MailboxLine | { error: string } {
  const line = text.trim();
  if (line === '') {
    return { error: 'Paste a line first.' };
  }
  const separator = separators.find((s) => line.includes(s)) ?? ':';
  const parts = line.split(separator).map((part) => part.trim());
  const user = parts.find((part) => emailRe.test(part));
  if (user === undefined) {
    return { error: 'The line holds no email address.' };
  }
  const clientId = parts.find((part) => clientIdRe.test(part));
  if (clientId === undefined) {
    return {
      error: 'The line holds no client ID, a GUID such as 00000000-0000-0000-0000-000000000000.',
    };
  }
  const refreshToken = parts
    .filter((part) => part !== user && part !== clientId)
    .reduce((longest, part) => (part.length > longest.length ? part : longest), '');
  if (refreshToken.length < minTokenLength) {
    return { error: 'The line holds no refresh token.' };
  }
  return { user, clientId, refreshToken };
}

/** The mailboxes of a pasted list, and the error of each line that does not read. */
export interface MailboxList {
  mailboxes: MailboxLine[];
  /** The number of each line, counted from 1, with its error. */
  errors: { line: number; error: string }[];
}

/**
 * Reads each non-empty line of text with parseMailboxLine. A second line of an
 * email that an earlier line has is dropped, so each mailbox shows once.
 */
export function parseMailboxList(text: string): MailboxList {
  const list: MailboxList = { mailboxes: [], errors: [] };
  text.split(/\r?\n/).forEach((line, i) => {
    if (line.trim() === '') {
      return;
    }
    const parsed = parseMailboxLine(line);
    if ('error' in parsed) {
      list.errors.push({ line: i + 1, error: parsed.error });
    } else if (!list.mailboxes.some((m) => m.user.toLowerCase() === parsed.user.toLowerCase())) {
      list.mailboxes.push(parsed);
    }
  });
  return list;
}
