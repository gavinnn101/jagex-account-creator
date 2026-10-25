import { expect, test, type APIRequestContext, type Page } from '@playwright/test';
import type { components } from '#lib/api.gen.d.ts';
import { phaseText } from '#lib/run/stats.ts';
import { expectNoAxeViolations, readJSON } from './helpers.ts';

type AccountPage = components['schemas']['AccountPage'];
type RunState = components['schemas']['RunState'];

// The path of each screen and the nav link that marks it.
const screens = [
  { path: '/', link: 'Run' },
  { path: '/logs', link: 'Logs' },
  { path: '/runs', link: 'Runs' },
  { path: '/accounts', link: 'Accounts' },
  { path: '/proxies', link: 'Proxies' },
  { path: '/checks', link: 'Checks' },
];

// The screens above and the detail of the newest seeded account, a sub-page of Accounts.
async function allScreens(request: APIRequestContext): Promise<typeof screens> {
  const { accounts } = await readJSON<AccountPage>(request, '/api/accounts');
  const id = accounts[0]?.id;
  expect(id).toBeDefined();
  return [...screens, { path: `/accounts/${String(id)}`, link: 'Accounts' }];
}

function badge(page: Page) {
  return page.getByRole('link', { name: /^Run phase: / });
}

// Opens path and waits until its data and the first run state are on screen.
async function open(page: Page, path: string): Promise<void> {
  await page.goto(path);
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
  await expect(badge(page)).not.toHaveAccessibleName('Run phase: Connecting');
}

test('the nav marks the link of each screen and of a sub-page', async ({ page, request }) => {
  for (const { path, link } of await allScreens(request)) {
    await open(page, path);
    const current = page.getByRole('navigation', { name: 'Main' }).locator('[aria-current="page"]');
    await expect(current, path).toHaveText([link]);
  }
});

test('the phase badge shows the phase of the server', async ({ page, request }) => {
  await open(page, '/logs');
  await expect(badge(page)).toHaveAttribute('href', '/');
  // Another file can start or stop the run of serve, so each read of the server and the badge repeats.
  await expect
    .poll(async () => {
      const { phase } = await readJSON<RunState>(request, '/api/run');
      const shown = (await badge(page).textContent())?.replace(/\s+/g, ' ').trim();
      return shown === `Run phase: ${phaseText[phase]}`;
    })
    .toBe(true);
});

test('a light choice stays after a reload', async ({ page }) => {
  await open(page, '/');
  const root = page.locator('html');
  const toggle = page.getByRole('button', { name: 'Light theme' });
  await expect(root).not.toHaveAttribute('data-theme');
  await expect(toggle).toHaveAttribute('aria-pressed', 'false');

  await toggle.click();
  await expect(root).toHaveAttribute('data-theme', 'light');
  await page.reload();
  // The toggle reads the attribute, not storage, so only the head script of app.html can press it.
  await expect(toggle).toHaveAttribute('aria-pressed', 'true');
  await expect(root).toHaveAttribute('data-theme', 'light');
  expect(await root.evaluate((html) => getComputedStyle(html).colorScheme)).toBe('light');

  await expectNoAxeViolations(page);
});

test.describe('at a phone width', () => {
  const width = 390;
  test.use({ viewport: { width, height: 844 } });

  test('no screen scrolls sideways', async ({ page, request }) => {
    // The page width of each screen wider than the viewport, so one failure names every such screen.
    const wide: Record<string, number> = {};
    for (const { path } of await allScreens(request)) {
      await open(page, path);
      // A table scrolls in its own region, so only the page width counts.
      const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
      if (scrollWidth > width) {
        wide[path] = scrollWidth;
      }
    }
    expect(wide).toEqual({});
    await open(page, '/');
    await expectNoAxeViolations(page);
  });
});
