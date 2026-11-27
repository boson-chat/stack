// The web client the wizard installed loads from https://chat.DOMAIN (names
// mapped to this machine; Pebble's certificate isn't trusted, so ignored),
// and the admin panel answers.
import { test, expect } from '@playwright/test';
import { DOMAIN, ADMIN, ADMIN_PASSWORD } from './lib';

test.use({ launchOptions: { args: [`--host-resolver-rules=MAP *.${DOMAIN} 127.0.0.1`] } });

test('the web client loads', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  const res = await page.goto(`https://chat.${DOMAIN}/`, { waitUntil: 'domcontentloaded' });
  expect(res?.status()).toBe(200);
  await page.waitForFunction(() => 'bosonShellReady' in window, null, { timeout: 60_000 });
  // From the page (the request API doesn't use the browser's host mapping).
  const cfg = await page.evaluate(() => fetch('/boson-config.js').then((r) => r.text()));
  expect(cfg).toContain(`"https://auth.${DOMAIN}"`);
  await expect(page.getByRole('button', { name: 'Continue without an account' })).toBeVisible();
  expect(errors).toEqual([]);
});

test('the admin panel answers', async ({ page }) => {
  const res = await page.goto(`https://admin.${DOMAIN}/`, { waitUntil: 'domcontentloaded' });
  expect(res?.status()).toBeLessThan(400);
});

// The wizard turned applying updates on (the default): the panel's Updates
// API says it can apply them (the updates profile's socket proxy).
test('the admin panel can apply updates', async ({ page }) => {
  await page.goto(`https://admin.${DOMAIN}/login`);
  await page.getByLabel(/username or email/i).fill(ADMIN);
  await page.getByLabel(/password/i).fill(ADMIN_PASSWORD);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Stack', level: 1 })).toBeVisible({ timeout: 60_000 });
  const st = await page.evaluate(() => fetch('/api/updates').then((r) => r.json()));
  expect(st.apply, JSON.stringify(st.apply)).toMatchObject({ enabled: true });
});
