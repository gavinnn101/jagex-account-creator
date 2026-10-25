import { chromium, defineConfig, devices } from '@playwright/test';
import { copyFileSync, mkdtempSync, rmSync, writeSync } from 'node:fs';
import { createServer, type AddressInfo } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { inspect } from 'node:util';

const bin = process.env.CREATOR_BIN;
if (bin === undefined || bin === '') {
  throw new Error(
    'CREATOR_BIN must name a built creator binary, for example: go build -o /tmp/creator ./cmd/creator',
  );
}

async function freePort(): Promise<string> {
  const server = createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const { port } = server.address() as AddressInfo;
  await new Promise((resolve) => server.close(resolve));
  return String(port);
}

// Playwright loads this file again in each worker, which must reuse the port and dir of the runner.
process.env.E2E_PORT ??= await freePort();
if (process.env.E2E_DIR === undefined) {
  const dir = mkdtempSync(join(tmpdir(), 'creator-e2e-'));
  process.env.E2E_DIR = dir;
  // The runner stops serve before it exits. Windows refuses to remove a dir that serve still holds.
  // A throw here leaves the exit code 0. stderr to a Windows terminal is async, so the write is sync.
  process.on('exit', () => {
    try {
      rmSync(dir, { recursive: true, force: true });
    } catch (error) {
      process.exitCode = 1;
      writeSync(2, `${inspect(error)}\n`);
    }
  });
  // serve imports the seed into its new database once, so a worker never writes it again.
  copyFileSync(
    join(import.meta.dirname, 'e2e', 'accounts.jsonl'),
    join(process.env.E2E_DIR, 'accounts.jsonl'),
  );
}
const baseURL = `http://127.0.0.1:${process.env.E2E_PORT}`;

export default defineConfig({
  testDir: 'e2e',
  forbidOnly: true,
  use: { baseURL },
  projects: [{ name: 'chromium', use: devices['Desktop Chrome'] }],
  webServer: {
    command: `"${bin}" -mode serve -listen 127.0.0.1:${process.env.E2E_PORT}`,
    cwd: process.env.E2E_DIR,
    url: `${baseURL}/api/run`,
    env: { CHROME_PATH: chromium.executablePath() },
    reuseExistingServer: false,
  },
});
