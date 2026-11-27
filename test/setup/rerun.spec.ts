// install.sh again on the installed server: the wizard finds the install,
// fills in its settings, keeps the admin and every secret, and applying the
// same answers changes nothing (run.sh compares .env and boson.yaml).
import { test, expect } from '@playwright/test';
import { DEV, DOMAIN, ADMIN, shot, enterCode, next } from './lib';

const CODE = process.env['SETUP_CODE'] ?? 'TEST-CODE';

test('re-run on an existing install is idempotent', async ({ page, request }) => {
  await enterCode(page, CODE);
  await page.locator('[data-flow=fresh]').click();
  await expect(page.getByRole('heading', { name: 'Your domain' })).toBeVisible();
  await expect(page.getByText('Found an existing install')).toBeVisible();
  await expect(page.getByLabel('Domain', { exact: true })).toHaveValue(DOMAIN);
  await expect(page.locator(`[data-host="irc.${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
  await shot(page, 'rerun-domain', 'rerun-');
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'TLS certificates' })).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Your admin account' })).toBeVisible();
  await expect(page.getByText(`${ADMIN} exists already and keeps its password`)).toBeVisible();
  await expect(page.getByLabel('Username')).toHaveValue(ADMIN);
  await expect(page.getByLabel('Password', { exact: true })).toBeDisabled();
  await shot(page, 'rerun-admin', 'rerun-');
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Features' })).toBeVisible();
  await expect(page.getByRole('switch', { name: /^Voice/ })).toBeChecked();
  // Read back from .env (the updates profile + the proxy URL).
  await expect(page.getByRole('switch', { name: /^Let the admin panel apply updates/ })).toBeChecked();
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Firewall and ports' })).toBeVisible();
  // Ports the stack holds now count as fine.
  if (!DEV) await expect(page.locator('[data-port="443/tcp"]')).toContainText('Used by this Boson install already');
  else await expect(page.locator('[data-port="443/tcp"]')).toBeVisible();
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  await expect(page.getByTestId('review-env')).toContainText('POSTGRES_PASSWORD=••••••••  # kept from your .env');
  await expect(page.getByTestId('review-env')).not.toContainText('new, random');
  // A re-run never upgrades: every version is the one in .env (updates
  // are the admin panel's job), and applying updates stays on.
  const versions = page.getByTestId('review-versions').locator('li');
  await expect(versions.first()).toBeVisible();
  for (const src of await versions.evaluateAll((els) => els.map((e) => e.getAttribute('data-source')))) expect(src).toBe('kept');
  await expect(page.getByTestId('review-updates')).toContainText('The admin panel applies updates');
  await expect(page.getByText('config/boson.yaml (unchanged)')).toBeVisible();
  await shot(page, 'rerun-review', 'rerun-');
  await page.getByTestId('install').click();
  await expect(page.getByRole('heading', { name: 'Applying your settings' })).toBeVisible();
  // Done, or a failed step (then fail with its error, not a timeout).
  const doneOrError = page.getByRole('heading', { name: 'Boson is running' }).or(page.getByTestId('install-error'));
  await expect(doneOrError.first()).toBeVisible({ timeout: 20 * 60_000 });
  if (await page.getByTestId('install-error').isVisible()) throw new Error('install failed: ' + (await page.getByTestId('install-error').innerText()));

  // The oper password was kept, so there's none to show.
  await expect(page.getByTestId('oper-password')).toHaveCount(0);
  await expect(page.locator('pre')).toHaveCount(0);
  await shot(page, 'rerun-done', 'rerun-');
  // The code was used: a second browser can't get in with it.
  const again = await request.post('/api/session', { data: { code: CODE } });
  expect(again.status()).toBe(403);
  await page.getByTestId('finish').click();
  await expect(page.getByText('Setup is switched off.')).toBeVisible();
});
