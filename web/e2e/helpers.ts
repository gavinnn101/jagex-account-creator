import AxeBuilder from '@axe-core/playwright';
import { expect, type APIRequestContext, type Locator, type Page } from '@playwright/test';
import { createServer, type AddressInfo, type Server, type Socket } from 'node:net';

// Sets data-theme on root to theme, or removes it for undefined, which shows dark.
function showTheme(page: Page, theme: string | undefined): Promise<void> {
  return page.evaluate((value) => {
    if (value === undefined) {
      delete document.documentElement.dataset.theme;
    } else {
      document.documentElement.dataset.theme = value;
    }
  }, theme);
}

// Runs axe in the theme on screen, then in the other theme. It restores the first theme at the end.
export async function expectNoAxeViolations(page: Page): Promise<void> {
  // A color in mid-transition gives axe a contrast that the screen never settles on. A theme
  // switch starts one, and so can a run of another test file that changes the state of serve.
  const still = await page.addStyleTag({ content: '* { transition: none !important; }' });
  const first = await page.evaluate(() => document.documentElement.dataset.theme);
  for (const theme of [first, first === 'light' ? 'dark' : 'light']) {
    await showTheme(page, theme);
    const axe = await new AxeBuilder({ page }).analyze();
    expect(axe.violations, `axe in the ${theme ?? 'dark'} theme`).toEqual([]);
  }
  await showTheme(page, first);
  // addStyleTag types its handle as a Node, and the node is the style element.
  await still.evaluate((style) => {
    (style as HTMLStyleElement).remove();
  });
}

// Opens each closed details section in scope with a click on its summary. A control in a closed
// section takes no fill or click, and getByRole does not find it.
export async function openSections(scope: Page | Locator): Promise<void> {
  const closed = scope.locator('details:not([open]) > summary');
  const count = await closed.count();
  for (let i = 0; i < count; i++) {
    await closed.first().click();
  }
  await expect(closed).toHaveCount(0);
}

export async function readJSON<T>(request: APIRequestContext, path: string): Promise<T> {
  const answer = await request.get(path);
  expect(answer.status()).toBe(200);
  return (await answer.json()) as T;
}

// The text of each cell of each body row of table, with each run of white space as one space and
// trimmed, as the screen shows it. A cell whose child is a time element gives its datetime value,
// because the shown text of a relative time changes as the clock moves.
export function shownRows(table: Locator): Promise<string[][]> {
  return table
    .locator('tbody tr')
    .evaluateAll((rows) =>
      rows.map((r) =>
        [...r.children].map(
          (cell) =>
            cell.querySelector(':scope > time[datetime]')?.getAttribute('datetime') ??
            cell.textContent.replace(/\s+/g, ' ').trim(),
        ),
      ),
    );
}

async function listen(server: Server): Promise<number> {
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  return (server.address() as AddressInfo).port;
}

// A port that had a listener and has none now, so a connection to it is refused.
export async function closedPort(): Promise<number> {
  const server = createServer();
  const port = await listen(server);
  await new Promise((resolve) => server.close(resolve));
  return port;
}

export interface AcceptingServer {
  port: number;
  /** Ends each connection, then closes the listener. */
  close: () => Promise<void>;
}

// A loopback listener that accepts each connection and never answers.
export async function acceptingServer(): Promise<AcceptingServer> {
  const sockets = new Set<Socket>();
  const server = createServer((socket) => {
    sockets.add(socket);
    // The client closes its end. A reset there is not a test failure.
    socket.on('error', () => undefined);
  });
  const port = await listen(server);
  const close = async (): Promise<void> => {
    for (const socket of sockets) {
      socket.destroy();
    }
    await new Promise((resolve) => server.close(resolve));
  };
  return { port, close };
}
