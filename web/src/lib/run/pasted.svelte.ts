import type { components } from '#lib/api.gen.d.ts';
import { parseMailboxLine } from './mailboxLine.ts';

type EmailConfig = components['schemas']['EmailConfig'];

/**
 * The pasted mailbox list of this browser tab, the email of the mailbox that
 * filled the login fields last, and the lowercase emails that already had an
 * account when the user pasted them. It outlives a visit to another screen, so
 * the Run screen gets the list back. A page reload clears it, so its refresh
 * tokens are stored nowhere new.
 */
export const pasted = $state({ text: '', picked: '', known: [] as string[] });

/**
 * Removes the mailboxes of users from the mailboxes list of email, and their
 * lines from the pasted text, so a later read of the text does not bring them
 * back. The match ignores case. When the login fields hold a removed mailbox,
 * the first mailbox left fills them.
 */
export function removeMailboxes(email: EmailConfig, users: readonly string[]): void {
  const gone = users.map((u) => u.toLowerCase());
  const isGone = (user: string): boolean => gone.includes(user.toLowerCase());
  pasted.text = pasted.text
    .split(/\r?\n/)
    .filter((line) => {
      const parsed = parseMailboxLine(line);
      return 'error' in parsed || !isGone(parsed.user);
    })
    .join('\n');
  const left = (email.mailboxes ?? []).filter((m) => !isGone(m.imap_user));
  if (left.length > 0) {
    email.mailboxes = left;
  } else {
    delete email.mailboxes;
  }
  const first = left[0];
  if (first !== undefined && isGone(email.imap_user)) {
    pasted.picked = first.imap_user;
    email.imap_user = first.imap_user;
    email.oauth2.client_id = first.oauth2.client_id;
    if (first.oauth2.refresh_token === undefined) {
      delete email.oauth2.refresh_token;
    } else {
      email.oauth2.refresh_token = first.oauth2.refresh_token;
    }
  }
}
