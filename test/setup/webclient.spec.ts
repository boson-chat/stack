// The web client the wizard installed loads from https://chat.DOMAIN (names
// mapped to this machine; Pebble's certificate isn't trusted, so ignored),
// and the admin panel answers.
import { test, expect } from '@playwright/test';
import { DOMAIN } from './lib';

test.use({ launchOptions: { args: [`--host-resolver-rules=MAP *.${DOMAIN} 127.0.0.1`] } });

test('the web client loads', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const res = await page.goto(`https://chat.${DOMAIN}/`, { waitUntil: 'domcontentloaded' });
  expect(res?.status()).toBe(200);
  await page.waitForFunction(() => 'bosonShellReady' in window, null, { timeout: 60_000 });
  const cfg = await (await page.request.get(`https://chat.${DOMAIN}/boson-config.js`)).text();
  expect(cfg).toContain(`"https://auth.${DOMAIN}"`);
  await expect(page.getByRole('button', { name: 'Continue without an account' })).toBeVisible();
  expect(errors).toEqual([]);
});

test('the admin panel answers', async ({ page }) => {
  const res = await page.goto(`https://admin.${DOMAIN}/`, { waitUntil: 'domcontentloaded' });
  expect(res?.status()).toBeLessThan(400);
});
