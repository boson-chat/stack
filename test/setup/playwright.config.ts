import { defineConfig } from '@playwright/test';

// The setup wizard end to end (test/setup/run.sh): drives the wizard that
// install.sh started at https://<runner ip>:8443 (self-signed: ignored).
// Also runs against the dev server (images/setup/cmd/devserver) with DEV=1.
export default defineConfig({
  testDir: '.',
  testMatch: /.*\.spec\.ts/,
  timeout: 25 * 60_000,
  expect: { timeout: process.env['DEV'] === '1' ? 8_000 : 30_000 },
  workers: 1,
  reporter: [['list']],
  use: {
    browserName: 'chromium',
    ignoreHTTPSErrors: true,
    baseURL: process.env['SETUP_URL'] ?? 'https://127.0.0.1:8443',
    viewport: { width: 1280, height: 900 },
    // The admin panel's default look (light follows the OS).
    colorScheme: 'dark',
    trace: 'retain-on-failure',
  },
});
