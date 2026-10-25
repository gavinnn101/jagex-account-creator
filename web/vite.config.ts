import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { playwright } from '@vitest/browser-playwright';
import { defineConfig } from 'vitest/config';

// Parallel Stryker workers race on one Vite optimizer dir, so each worker gets its own.
const strykerWorker = process.env.STRYKER_MUTATOR_WORKER;

export default defineConfig({
  ...(strykerWorker === undefined
    ? {}
    : { cacheDir: `node_modules/.vite-stryker-${strykerWorker}` }),
  plugins: [
    tailwindcss(),
    sveltekit({
      adapter: adapter({
        pages: 'dist/spa',
        assets: 'dist/spa',
        fallback: 'index.html',
        strict: true,
      }),
    }),
  ],
  server: {
    proxy: { '/api': 'http://127.0.0.1:8080' },
  },
  test: {
    include: ['src/**/*.test.ts'],
    // Each wait gets a bound inside the guard rule, 2 s to 10 s.
    // So a mutant that hides an element fails below the per-mutant timeout of Stryker.
    // An actionTimeout stops expect.element from taking the rest of the test timeout, so it uses the poll timeout.
    // Limitation: an explicit timeout option, or a per-test timeout over 10 s, bypasses these bounds. It fails toward a slow mutation run.
    testTimeout: 10000,
    // The module cache writes node_modules/.vitest-cache, and the tree check of the web coverage gate refuses a test write there.
    fsModuleCache: false,
    expect: { poll: { timeout: 5000 } },
    browser: {
      enabled: true,
      headless: true,
      // The tests state exact local times, so the locale and the zone are fixed.
      // A zone off UTC fails a test that assumes UTC.
      provider: playwright({
        actionTimeout: 5000,
        contextOptions: { locale: 'en-US', timezoneId: 'Asia/Kolkata' },
      }),
      instances: [{ browser: 'chromium' }],
    },
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,svelte}'],
      reporter: ['json', 'text'],
    },
  },
});
