import { expect, test, type Locator } from '@playwright/test';
import type { components } from '#lib/api.gen.d.ts';
import { mailboxDigest } from '#lib/digest.ts';
import { expectNoAxeViolations, readJSON } from './helpers.ts';

type ChromeCheck = components['schemas']['ChromeCheck'];
type Config = components['schemas']['Config'];

// The term and the value of each row of the list in scope, with each run of white space as one space.
async function shownTerms(scope: Locator): Promise<Record<string, string>> {
  const pairs = await scope.locator('dl > dt').evaluateAll((terms) => {
    const text = (e: Element | null) => (e?.textContent ?? '').replace(/\s+/g, ' ').trim();
    return terms.map((dt): [string, string] => [text(dt), text(dt.nextElementSibling)]);
  });
  return Object.fromEntries(pairs);
}

test('the Checks screen shows the answers of the server', async ({ page, request }) => {
  const check = await readJSON<ChromeCheck>(request, '/api/checks/chrome');
  // Another test file can save the config, so the test reads the answer that the page loaded.
  const configAnswer = page.waitForResponse((r) => new URL(r.url()).pathname === '/api/config');

  await page.goto('/checks');
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Checks');
  const config = (await (await configAnswer).json()) as Config;

  const chrome = page.getByRole('region', { name: 'Chrome', exact: true });
  const differs = check.mismatch === true;
  const expected: Record<string, string> = check.ok
    ? {
        Status: 'Found',
        Version: `${check.version ?? ''} ${differs ? 'Differs' : 'Matches'}`,
      }
    : { Status: 'Not found', Error: check.error ?? '' };
  expected['Request shape major'] = String(check.shape_major);
  expect(await shownTerms(chrome)).toEqual(expected);
  const tones = check.ok ? ['success', differs ? 'warning' : 'success'] : ['danger'];
  const badges = chrome.locator('[data-tone]');
  await expect(badges).toHaveCount(tones.length);
  expect(await badges.evaluateAll((all) => all.map((b) => b.getAttribute('data-tone')))).toEqual(
    tones,
  );
  await expect(page.getByRole('alert')).toHaveCount(differs ? 1 : 0);

  const mailbox = page.getByRole('region', { name: 'Mailbox', exact: true });
  const digest = mailboxDigest(config);
  expect(await shownTerms(mailbox)).toEqual({ 'Saved mailbox': digest });
  // No click: after the run test saves imap.example.com, a check connects to a real host.
  const button = mailbox.getByRole('button', { name: 'Check mailbox', exact: true });
  await (digest === 'Not set' ? expect(button).toBeDisabled() : expect(button).toBeEnabled());

  await expectNoAxeViolations(page);
});
