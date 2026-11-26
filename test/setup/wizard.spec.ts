// A fresh install through the wizard, with the error cases on the way:
// a wrong code, DNS pointing elsewhere, a UDP port the firewall drops (voice
// turned off, then on again once it's open), and a service that can't
// start (port taken) retried. test/setup/run.sh runs install.sh first and
// passes the code it printed ($SETUP_CODE).
import { test, expect } from '@playwright/test';
import { DEV, DOMAIN, BAD_DOMAIN, ADMIN, ADMIN_EMAIL, ADMIN_PASSWORD, shot, hook, enterCode, next } from './lib';

const CODE = process.env['SETUP_CODE'] ?? 'TEST-CODE';
const ACME_URL = process.env['SETUP_ACME_URL'] ?? '';
const SMTP_HOST = process.env['SETUP_SMTP_HOST'] ?? '';
const SMTP_PORT = process.env['SETUP_SMTP_PORT'] ?? '1025';

test.describe.configure({ mode: 'serial' });

test('new install through the wizard', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));

  // ---- the code ----
  await enterCode(page, 'WRNG-CODE');
  await expect(page.getByRole('alert')).toContainText('not right');
  await shot(page, 'code-wrong');
  await page.getByLabel('Setup code').fill(CODE.toLowerCase());
  await page.getByRole('button', { name: 'Continue' }).click();

  // ---- the two paths ----
  await expect(page.getByRole('heading', { name: 'What are you setting up?' })).toBeVisible();
  await expect(page.locator('[data-flow=addon]')).toContainText('Coming soon');
  await shot(page, 'chooser');
  await page.locator('[data-flow=addon]').click();
  await expect(page.getByRole('heading', { name: 'Add Boson to my existing IRC server' })).toBeVisible();
  await shot(page, 'addon-stub');
  await page.getByRole('link', { name: '← Back' }).click();
  await page.locator('[data-flow=fresh]').click();

  // ---- domain: elsewhere is blocked ----
  await expect(page.getByRole('heading', { name: 'Your domain' })).toBeVisible();
  await page.getByLabel('Network name').fill('E2E Net');
  await page.getByLabel('Domain', { exact: true }).fill(BAD_DOMAIN);
  await expect(page.locator(`[data-host="irc.${BAD_DOMAIN}"]`)).toHaveAttribute('data-status', 'elsewhere');
  await expect(page.getByRole('alert').filter({ hasText: 'DNS points somewhere else' })).toContainText('which is not this server');
  await expect(next(page)).toBeDisabled();
  await shot(page, 'domain-elsewhere');

  // ---- domain: right ----
  await page.getByLabel('Domain', { exact: true }).fill(DOMAIN);
  await expect(page.locator(`[data-host="irc.${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
  await expect(page.getByText('Every name points at this server.')).toBeVisible();
  await expect(next(page)).toBeEnabled();
  await shot(page, 'domain-ok');
  await next(page).click();

  // ---- TLS ----
  await expect(page.getByRole('heading', { name: 'TLS certificates' })).toBeVisible();
  await page.getByLabel('Email for Let’s Encrypt').fill(ADMIN_EMAIL);
  if (ACME_URL) {
    await page.getByText('Another ACME server').click();
    await page.getByLabel('ACME directory URL').fill(ACME_URL);
  }
  await shot(page, 'tls');
  await next(page).click();

  // ---- admin ----
  await expect(page.getByRole('heading', { name: 'Your admin account' })).toBeVisible();
  await page.getByLabel('Username').fill(ADMIN);
  await page.getByLabel('Email').fill(ADMIN_EMAIL);
  await page.getByLabel('Password', { exact: true }).fill('short');
  await page.getByLabel('Password again').fill('short');
  await next(page).click();
  await expect(page.getByText('Use at least 10 characters.')).toBeVisible();
  await page.getByLabel('Password', { exact: true }).fill(ADMIN_PASSWORD);
  await page.getByLabel('Password again').fill(ADMIN_PASSWORD);
  await shot(page, 'admin');
  await next(page).click();

  // ---- features ----
  await expect(page.getByRole('heading', { name: 'Features' })).toBeVisible();
  const voice = page.getByRole('switch', { name: /^Voice/ });
  await expect(voice).toBeChecked();
  await expect(page.getByRole('switch', { name: /^File uploads/ })).toBeChecked();
  if (SMTP_HOST) {
    await page.getByLabel('Mail server').fill(SMTP_HOST);
    await page.getByLabel('Port').fill(SMTP_PORT);
    await page.getByLabel('Sender').fill(`E2E Net <noreply@${DOMAIN}>`);
    await page.getByLabel('Security').selectOption('none');
    await page.getByTestId('smtp-test').click();
    await expect(page.getByText('Sent. Check the inbox')).toBeVisible();
  }
  await shot(page, 'features');
  await next(page).click();

  // ---- ports: the firewall drops 7700/udp ----
  await expect(page.getByRole('heading', { name: 'Firewall and ports' })).toBeVisible();
  hook('block-udp');
  await page.getByTestId('ports-recheck').click();
  const udp = page.locator('[data-port="7700/udp"]');
  await expect(udp).toHaveAttribute('data-level', 'warn');
  await expect(udp).toContainText('drops 7700/udp');
  await expect(page.locator('[data-port="443/tcp"]')).toHaveAttribute('data-level', 'ok');
  await expect(page.getByText('What this can’t check.')).toBeVisible();
  await shot(page, 'ports-udp-blocked');
  await page.getByTestId('off-voice').click();
  await expect(page.locator('[data-port="443/tcp"]')).toBeVisible();
  await expect(udp).toHaveCount(0);
  await shot(page, 'ports-voice-off');
  // Opened again: voice back on, the check passes.
  hook('unblock-udp');
  await page.getByRole('link', { name: '← Back' }).click();
  await page.getByRole('switch', { name: /^Voice/ }).check({ force: true });
  await next(page).click();
  if (!DEV) await expect(udp).toHaveAttribute('data-level', 'ok');
  await shot(page, 'ports-ok');
  await next(page).click();

  // ---- review ----
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  const env = page.getByTestId('review-env');
  await expect(env).toContainText('POSTGRES_PASSWORD=••••••••  # new, random');
  await expect(env).toContainText(`AUTH_HOST=auth.${DOMAIN}`);
  await expect(env).toContainText('COMPOSE_PROFILES=irc,auth,api,web,admin,metrics,voice,files');
  await expect(page.getByTestId('review-config')).toContainText('registration: open');
  await shot(page, 'review');

  // ---- install: a service can't start (6697 is taken), then retry ----
  hook('occupy-6697');
  await page.getByTestId('install').click();
  await expect(page.getByRole('heading', { name: 'Installing Boson' })).toBeVisible();
  await expect(page.locator('[data-step=start]')).toHaveAttribute('data-status', 'failed', { timeout: 15 * 60_000 });
  await expect(page.getByTestId('install-error')).toContainText(/6697/);
  await expect(page.getByTestId('retry')).toBeVisible();
  await shot(page, 'install-failed');
  hook('free-6697');
  await page.getByTestId('retry').click();
  await expect(page.locator('[data-step=start]')).toHaveAttribute('data-status', /running|done/);
  await expect(page.locator('[data-step=health]')).toHaveAttribute('data-status', /running|done/, { timeout: 5 * 60_000 });
  await shot(page, 'install-running');

  // ---- done ----
  await expect(page.getByRole('heading', { name: 'Boson is running' })).toBeVisible({ timeout: 20 * 60_000 });
  await expect(page.getByTestId('link-web')).toHaveAttribute('href', `https://chat.${DOMAIN}/`);
  await expect(page.getByTestId('link-admin')).toHaveAttribute('href', `https://admin.${DOMAIN}/`);
  await expect(page.getByTestId('oper-password')).toHaveText(/^[A-Za-z0-9]{20}$/);
  await shot(page, 'done');
  await page.getByTestId('finish').click();
  await expect(page.getByText('Setup is switched off.')).toBeVisible();
  expect(errors).toEqual([]);

  // The wizard is off: the code and the session are dead.
  const res = await page.request.get('/api/session').catch(() => null);
  if (res) expect((await res.json()).closed).toBe(true);
});
