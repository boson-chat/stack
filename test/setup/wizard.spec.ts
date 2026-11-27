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
  await expect(page.locator('[data-flow=addon]')).not.toContainText('Coming soon');
  await shot(page, 'chooser');
  // The add-on path is its own e2e (addon.spec.ts); here: it opens, and back.
  await page.locator('[data-flow=addon]').click();
  await expect(page.getByRole('heading', { name: 'Your IRC server' })).toBeVisible();
  await page.getByRole('link', { name: '← Start' }).click();
  await page.locator('[data-flow=fresh]').click();

  // ---- domain: elsewhere is blocked ----
  await expect(page.getByRole('heading', { name: 'Your domain' })).toBeVisible();
  await page.getByLabel('Network name').fill('E2E Net');
  await page.getByLabel('Domain', { exact: true }).fill(BAD_DOMAIN);
  await expect(page.getByText('it’s also the IRC network name'.replace('’', "'"))).toBeVisible();
  await page.getByLabel('Network name').fill('E2E-Net');
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
  // App discovery (boson.json on the bare domain) is on by default, so the
  // domain itself is checked too. Off: what to publish elsewhere.
  const discovery = page.getByRole('switch', { name: /^App discovery/ });
  await expect(discovery).toBeChecked();
  await expect(page.locator(`[data-host="${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
  await discovery.uncheck({ force: true });
  await expect(page.getByTestId('boson-json')).toContainText(`"host": "irc.${DOMAIN}"`);
  await expect(page.locator(`[data-host="${DOMAIN}"]`)).toHaveCount(0);
  await shot(page, 'domain-discovery-off');
  await discovery.check({ force: true });
  await expect(page.getByTestId('boson-json')).toHaveCount(0);
  await expect(page.locator(`[data-host="${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
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
  // Outgoing email: recommended while sign-up is open, with what skipping
  // it means; not needed (no warning) while sign-up is closed.
  await expect(page.getByTestId('smtp-need')).toHaveText('recommended');
  await expect(page.getByTestId('smtp-skip-warning')).toContainText('no email verification');
  await expect(page.getByTestId('smtp-skip-warning')).toContainText('no password reset by email');
  await page.getByRole('group', { name: 'Sign-up' }).getByText('Closed').click();
  await expect(page.getByTestId('smtp-need')).toHaveText('not needed');
  await expect(page.getByTestId('smtp-skip-warning')).toBeHidden();
  await expect(page.getByTestId('smtp-not-needed')).toBeVisible();
  await shot(page, 'features-smtp-not-needed');
  await page.getByRole('group', { name: 'Sign-up' }).getByText('Open', { exact: true }).click();
  await expect(page.getByTestId('smtp-need')).toHaveText('recommended');
  if (SMTP_HOST) {
    await page.getByLabel('Mail server').fill(SMTP_HOST);
    await expect(page.getByTestId('smtp-skip-warning')).toBeHidden();
    await page.getByLabel('Port').fill(SMTP_PORT);
    await page.getByLabel('Sender').fill(`E2E Net <noreply@${DOMAIN}>`);
    await page.getByLabel('Security').selectOption('none');
    await page.getByTestId('smtp-test').click();
    await expect(page.getByText('Sent. Check the inbox')).toBeVisible();
  }
  // Accounts from other servers: verified servers by default; an empty
  // allowlist is refused.
  // Applying updates from the admin panel: on by default, with the warning.
  const applyUpdates = page.getByRole('switch', { name: /^Let the admin panel apply updates/ });
  await expect(applyUpdates).toBeChecked();
  await expect(page.getByText('root-equivalent access to this server')).toBeVisible();
  // Usage & health stats: on by default, the why next to the switch; this
  // test install turns it off (the one-click opt-out), so CI never reports.
  const usage = page.getByRole('switch', { name: /^Send anonymous usage & health stats/ });
  await expect(usage).toBeChecked();
  await expect(page.getByTestId('usage-why')).toContainText('to make Boson better');
  await usage.click();
  await expect(usage).not.toBeChecked();
  const fed = page.getByRole('group', { name: 'Accept accounts from' });
  await expect(fed.getByRole('radio', { name: /^Verified servers/ })).toBeChecked();
  await shot(page, 'features');
  await fed.getByText('Only servers I list').click();
  await expect(page.getByLabel('Servers to accept')).toBeVisible();
  await next(page).click();
  await expect(page.locator('.field .err').filter({ hasText: 'List at least one server' })).toBeVisible();
  await shot(page, 'features-allowlist-empty');
  await page.getByLabel('Servers to accept').fill('boson.chat, friends.example.org');
  await shot(page, 'features-allowlist');
  await fed.getByText('Verified servers').click();
  await expect(page.getByLabel('Servers to accept')).toBeHidden();
  await next(page).click();

  // ---- ports: the firewall drops 7700/udp, and node-exporter from the stack network ----
  await expect(page.getByRole('heading', { name: 'Firewall and ports' })).toBeVisible();
  hook('block-udp');
  hook('block-node');
  await page.getByTestId('ports-recheck').click();
  const udp = page.locator('[data-port="7700/udp"]');
  await expect(udp).toHaveAttribute('data-level', 'warn');
  await expect(udp).toContainText('drops 7700/udp');
  // The dev server has no probe container: no node-exporter row.
  const node = page.locator('[data-port="10.203.53.1:9100/tcp"]');
  if (!DEV) {
    await expect(node).toHaveAttribute('data-level', 'warn');
    await expect(node.getByTestId('port-command')).toHaveText(
      "sudo ufw allow from 10.203.53.0/24 to 10.203.53.1 port 9100 proto tcp comment 'boson: metrics -> node-exporter'",
    );
  }
  await expect(page.locator('[data-port="443/tcp"]')).toHaveAttribute('data-level', 'ok');
  await expect(page.getByText('What this can’t check.')).toBeVisible();
  await shot(page, 'ports-udp-blocked');
  await page.getByTestId('off-voice').click();
  await expect(page.locator('[data-port="443/tcp"]')).toBeVisible();
  await expect(udp).toHaveCount(0);
  await shot(page, 'ports-voice-off');
  // Opened again: voice back on, the check passes.
  hook('unblock-udp');
  hook('unblock-node');
  await page.getByRole('link', { name: '← Back' }).click();
  await page.getByRole('switch', { name: /^Voice/ }).check({ force: true });
  await next(page).click();
  if (!DEV) await expect(udp).toHaveAttribute('data-level', 'ok');
  if (!DEV) await expect(node).toHaveAttribute('data-level', 'ok');
  await shot(page, 'ports-ok');
  await next(page).click();

  // ---- review ----
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  const env = page.getByTestId('review-env');
  await expect(env).toContainText('POSTGRES_PASSWORD=••••••••  # new, random');
  await expect(env).toContainText(`AUTH_HOST=auth.${DOMAIN}`);
  await expect(env).toContainText('COMPOSE_PROFILES=irc,auth,api,web,admin,metrics,voice,files');
  await expect(env).toContainText('ADMIN_UPDATES_DOCKER_PROXY_URL=http://docker-proxy-updates:2375');
  await expect(env).toContainText(/COMPOSE_PROFILES=.*,updates/);
  await expect(page.getByTestId('review-updates')).toContainText('The admin panel applies updates');
  await expect(env).toContainText('TELEMETRY=off');
  await expect(page.getByTestId('review-usage')).toContainText('usage & health stats: off');
  // The image versions: every component, its tag in .env; the newest
  // release (or the pinned default, with a note, when GitHub can't be asked).
  const versions = page.getByTestId('review-versions');
  for (const v of ['AUTH_IMAGE', 'API_IMAGE', 'WEB_IMAGE', 'ADMIN_IMAGE', 'QUICVOICE_IMAGE', 'FILES_IMAGE']) {
    const row = versions.locator(`li[data-env="${v}"]`);
    await expect(row).toHaveAttribute('data-source', /^(newest|default)$/);
    const image = (await row.locator('.what').textContent()) ?? '';
    await expect(env).toContainText(`${v}=${image}`);
  }
  await expect(versions.locator('li[data-env="EVRD_IMAGE"]')).toHaveCount(0);
  await expect(page.getByTestId('review-config')).toContainText('registration: open');
  await expect(page.getByTestId('review-config')).toContainText(/federation:\s+mode: directory/);
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

  // ---- a service still starting: its Logs panel (dev server: ergo waits
  // for its certificate, Traefik can't get one) ----
  if (DEV) {
    await expect(page.locator('[data-service=ergo] .waiting')).toContainText('TLS certificate');
    await page.getByTestId('logs-toggle-ergo').click();
    await expect(page.getByTestId('logs-toggle-ergo')).toHaveAttribute('aria-expanded', 'true');
    await expect(page.getByTestId('logs-ergo')).toContainText("wget: can't connect to remote host");
    await expect(page.getByTestId('logs-text-ergo')).toHaveText('(no output yet)');
    await expect(page.getByTestId('warning-acme')).toContainText("Traefik can't get certificates");
    await expect(page.getByTestId('warning-acme')).toContainText('tls: failed to verify certificate');
    await page.getByTestId('logs-toggle-traefik').click();
    await expect(page.getByTestId('logs-text-traefik')).toContainText('Unable to obtain ACME certificate');
    await shot(page, 'install-service-logs');
    await page.getByTestId('logs-toggle-traefik').click();
    await expect(page.getByTestId('logs-traefik')).toBeHidden();
    // Only this install's services.
    expect((await page.request.get('/api/flows/fresh/install/services/..%2F..%2Fetc/logs')).status()).toBe(404);
  }

  // ---- done ----
  // Done, or a failed step (then fail with its error, not a timeout).
  const doneOrError = page.getByRole('heading', { name: 'Boson is running' }).or(page.getByTestId('install-error'));
  await expect(doneOrError.first()).toBeVisible({ timeout: 20 * 60_000 });
  if (await page.getByTestId('install-error').isVisible()) throw new Error('install failed: ' + (await page.getByTestId('install-error').innerText()));

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
