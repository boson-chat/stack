// "Add Boson to my existing IRC server" through the wizard, against one
// test/ircds pairing (test/setup/run-addon.sh starts it and install.sh):
// the server step (a wrong RPC token first, where there's an RPC), the
// capability table (= pkg/probe/testdata/golden/<pairing>.json), the bridge,
// the account mode (the recommended one, or SETUP_ACCOUNTS), the fresh
// flow's steps, then the snippets: Re-check stays red until they're
// applied to the real server (test/ircds/run.sh apply), then green, review,
// install, done.
import { test, expect } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DEV, DOMAIN, ADMIN, ADMIN_EMAIL, ADMIN_PASSWORD, shot, enterCode, next } from './lib';

const CODE = process.env['SETUP_CODE'] ?? 'TEST-CODE';
const PAIRING = process.env['SETUP_PAIRING'] ?? 'ngircd-anope';
const IRC_HOST = process.env['SETUP_IRC_HOST'] ?? '127.0.0.1';
const IRC_PORT = process.env['SETUP_IRC_PORT'] ?? '16697';
const RPC_URL = process.env['SETUP_RPC_URL'] ?? '';
const RPC_TOKEN = process.env['SETUP_RPC_TOKEN'] ?? '';
const NETWORK = process.env['SETUP_NETWORK'] ?? '';
// boson | nickserv: the account mode to install with ("" = the recommended one).
const ACCOUNTS = process.env['SETUP_ACCOUNTS'] ?? '';
// The recommended mode the wizard must pick (decision order).
const WANT_RECOMMENDED = process.env['SETUP_WANT_RECOMMENDED'] ?? '';
const NICKSERV_ADMIN = process.env['SETUP_NICKSERV_ADMIN'] ?? '';
const ACME_URL = process.env['SETUP_ACME_URL'] ?? '';
const REACT = process.env['SETUP_REACT'] === '1';
// Accept accounts only from these servers ("" = keep the default, verified
// servers): the federation e2e lets home.test in (addon-federation.py).
const FED_ALLOW = process.env['SETUP_FEDERATION_ALLOW'] ?? '';
const ROOT = path.join(__dirname, '..', '..');
const P = `addon-${PAIRING}-`;

function golden(name: string): unknown {
  return JSON.parse(readFileSync(path.join(ROOT, 'pkg/probe/testdata/golden', name), 'utf8'));
}

function ircds(cmd: string, file?: string): void {
  execFileSync('sh', [path.join(ROOT, 'test/ircds/run.sh'), cmd, PAIRING, ...(file ? [file] : [])], { stdio: 'inherit' });
}

test.describe.configure({ mode: 'serial' });

test(`add Boson to ${PAIRING} through the wizard`, async ({ page, context }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await context.grantPermissions(['clipboard-read', 'clipboard-write']).catch(() => {});
  ircds('reset');

  await enterCode(page, CODE);
  await expect(page.getByRole('heading', { name: 'What are you setting up?' })).toBeVisible();
  await expect(page.locator('[data-flow=addon]')).not.toContainText('Coming soon');
  await page.locator('[data-flow=addon]').click();

  // ---- the server ----
  await expect(page.getByRole('heading', { name: 'Your IRC server' })).toBeVisible();
  await page.getByLabel('IRC server', { exact: true }).fill(IRC_HOST);
  await page.getByLabel('Port', { exact: true }).fill(IRC_PORT);
  await expect(page.getByRole('switch', { name: /^TLS/ })).toBeChecked();
  if (RPC_URL) {
    await page.getByText('Services RPC (optional)').click();
    await page.getByLabel('RPC address').fill(RPC_URL);
    await page.getByLabel('Test account (optional)').fill('bosonprobe');
    await page.getByLabel('Its password').fill('probe-fixture-pass');
    if (RPC_TOKEN) {
      // A wrong token: a clear error, and the way on.
      await page.getByLabel('RPC token (Anope)').fill('not-the-token');
      await next(page).click();
      await expect(page.getByRole('alert').filter({ hasText: 'Services RPC' })).toContainText('refused the token', { timeout: 90_000 });
      await shot(page, 'rpc-wrong-token', P);
      await page.getByLabel('RPC token (Anope)').fill(RPC_TOKEN);
    }
  }
  await shot(page, 'server', P);
  await next(page).click();

  // ---- what Boson gets: the golden table ----
  await expect(page.getByRole('heading', { name: 'What Boson users get' })).toBeVisible({ timeout: 90_000 });
  const table = JSON.parse(await page.getByTestId('cap-table').textContent() ?? '{}');
  expect(table).toEqual(golden(`${PAIRING}.json`));
  for (const f of table.features) {
    await expect(page.locator(`[data-feature="${f.feature}"]`)).toHaveAttribute('data-status', f.status);
  }
  await shot(page, 'capabilities', P);
  await page.getByRole('button', { name: 'Continue' }).click();

  // ---- the bridge, explained ----
  await expect(page.getByRole('heading', { name: 'The bridge' })).toBeVisible();
  await expect(page.getByText('What it is:')).toBeVisible();
  const ip = await page.getByLabel('This server’s address, as your IRC server sees it').inputValue();
  expect(ip).toMatch(/^[0-9a-f.:]+$/);
  await shot(page, 'bridge', P);
  await page.getByRole('button', { name: 'Continue' }).click();

  // ---- accounts: the decision order, then the mode to test ----
  await expect(page.getByRole('heading', { name: 'Accounts' })).toBeVisible();
  if (WANT_RECOMMENDED) {
    await expect(page.getByText(`Recommended: ${WANT_RECOMMENDED === 'nickserv' ? 'NickServ accounts' : 'Boson accounts'}.`)).toBeVisible();
  }
  if (ACCOUNTS) {
    const nickserv = page.locator('input[name=accounts][value=nickserv]');
    if (ACCOUNTS === 'nickserv') await nickserv.check({ force: true });
    else await page.locator('input[name=accounts][value=boson]').check({ force: true });
  }
  await shot(page, 'accounts', P);
  const mode = await page.locator('input[name=accounts]:checked').getAttribute('value');
  await page.getByRole('button', { name: 'Continue' }).click();

  // ---- the fresh flow's steps ----
  await expect(page.getByRole('heading', { name: 'Your domain' })).toBeVisible();
  if (NETWORK) await expect(page.getByLabel('Network name')).toHaveValue(NETWORK);
  await page.getByLabel('Domain', { exact: true }).fill(DOMAIN);
  await expect(page.locator(`[data-host="irc.${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
  // boson.json from the bare domain: it names the bridge as the IRC server.
  await page.getByRole('switch', { name: /^Also serve app discovery/ }).check({ force: true });
  await expect(page.locator(`[data-host="${DOMAIN}"]`)).toHaveAttribute('data-status', 'ok');
  await expect(page.locator(`[data-host="irc.${DOMAIN}"]`)).toContainText('the bridge');
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'TLS certificates' })).toBeVisible();
  await page.getByLabel('Email for Let’s Encrypt').fill(ADMIN_EMAIL);
  if (ACME_URL) {
    await page.getByText('Another ACME server').click();
    await page.getByLabel('ACME directory URL').fill(ACME_URL);
  }
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Your admin account' })).toBeVisible();
  if (mode === 'nickserv') {
    await page.getByLabel('NickServ account').fill(NICKSERV_ADMIN || 'bosonprobe');
    await page.getByLabel('Email').fill(ADMIN_EMAIL);
    await expect(page.getByLabel('Password', { exact: true })).toBeHidden();
  } else {
    await page.getByLabel('Username').fill(ADMIN);
    await page.getByLabel('Email').fill(ADMIN_EMAIL);
    await page.getByLabel('Password', { exact: true }).fill(ADMIN_PASSWORD);
    await page.getByLabel('Password again').fill(ADMIN_PASSWORD);
  }
  await shot(page, 'admin', P);
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Features' })).toBeVisible();
  await expect(page.getByRole('switch', { name: /^Guests Not on this path/ })).toBeDisabled();
  // Federated sign-in: the same choice as on a new server, verified servers
  // by default, with a note on how remote users reach the IRC server.
  const fed = page.getByRole('group', { name: 'Accept accounts from' });
  await expect(fed).toBeVisible();
  await expect(fed.getByRole('radio', { name: /^Verified servers/ })).toBeChecked();
  await expect(page.getByTestId('federation-addon-note')).toContainText('through the bridge, without a services account there');
  await shot(page, 'features', P);
  if (FED_ALLOW) {
    await fed.getByText('Only servers I list').click();
    await page.getByLabel('Servers to accept').fill(FED_ALLOW);
    await shot(page, 'features-federation', P);
  }
  await next(page).click();
  await expect(page.getByRole('heading', { name: 'Firewall and ports' })).toBeVisible();
  await next(page).click();

  // ---- the config for their server ----
  const recheck = async () => {
    await page.getByTestId('recheck').click();
    await expect(page.getByTestId('checks-box')).toHaveAttribute('data-state', 'checking');
    await expect(page.getByTestId('checks-box')).toHaveAttribute('data-state', 'done', { timeout: 90_000 });
  };
  await expect(page.getByRole('heading', { name: 'Your server’s config' })).toBeVisible();
  await expect(page.locator('[data-snippet=webirc]')).toBeVisible();
  // Not applied yet: Re-check stays red, with the fix.
  await recheck();
  await expect(page.locator('[data-check=webirc]')).toHaveAttribute('data-ok', 'false', { timeout: 90_000 });
  await expect(page.locator('[data-check=limits]')).toHaveAttribute('data-ok', 'false');
  await expect(page.getByTestId('fix-webirc')).toContainText('snippet');
  await expect(next(page)).toBeDisabled();
  if (PAIRING.startsWith('ngircd')) {
    await expect(page.locator('[data-snippet-box=limits]')).toContainText('removes the per-address connection limit for every client');
  }
  await shot(page, 'snippets-red', P);

  // The copy button puts exactly the snippet on the clipboard.
  await page.getByTestId('copy-webirc').click();
  const clip = await page.evaluate(() => navigator.clipboard.readText()).catch(() => null);
  if (clip !== null) expect(clip).toBe(await page.locator('[data-snippet=webirc]').textContent());

  const dir = mkdtempSync(path.join(os.tmpdir(), 'boson-snippets-'));
  const apply = async (ids: string[] | null) => {
    for (const pre of await page.locator('[data-snippet-box] pre[data-snippet]').all()) {
      const id = (await pre.getAttribute('data-snippet'))!;
      if (ids ? !ids.includes(id) : id === 'services-rpc' || id.startsWith('react')) continue;
      const file = path.join(dir, `${id}.conf`);
      writeFileSync(file, (await pre.textContent()) ?? '');
      ircds((await pre.getAttribute('data-first')) === 'true' ? 'apply-first' : 'apply', file);
    }
  };
  await apply(null);
  await recheck();
  await expect(page.locator('[data-check=webirc]')).toHaveAttribute('data-ok', 'true', { timeout: 90_000 });
  await expect(page.locator('[data-check=limits]')).toHaveAttribute('data-ok', 'true');
  // The table with WEBIRC in place is the golden one too.
  const after = await (await page.request.get('/api/flows/addon/probe')).json();
  expect(after.result.table).toEqual(golden(`${PAIRING}.webirc.json`));
  if (REACT) {
    await page.getByRole('switch', { name: /^Also allow reactions/ }).check({ force: true });
    await expect(page.locator('[data-snippet=react]')).toBeVisible();
    await recheck();
    await expect(page.locator('[data-check=reactions]')).toHaveAttribute('data-ok', 'false', { timeout: 90_000 });
    // The fixture has the module built (test/ircds/unrealircd/Dockerfile): load it.
    await apply(['react']);
    await recheck();
    await expect(page.locator('[data-check=reactions]')).toHaveAttribute('data-ok', 'true', { timeout: 90_000 });
  }
  await expect(next(page)).toBeEnabled();
  await shot(page, 'snippets-green', P);
  await next(page).click();

  // ---- review ----
  await expect(page.getByRole('heading', { name: 'Review' })).toBeVisible();
  const env = page.getByTestId('review-env');
  await expect(env).toContainText(/COMPOSE_PROFILES=bridge,auth,api,web,admin,metrics/);
  await expect(env).not.toContainText('COMPOSE_PROFILES=irc');
  await expect(env).toContainText('BRIDGE_TOKEN=••••••••');
  await expect(env).toContainText(`ACCOUNT_BACKEND=${mode === 'nickserv' ? 'nickserv' : 'local'}`);
  await expect(page.locator('[data-path="bridge/evrd.conf"]')).toContainText(`managed_server ${IRC_HOST} +${IRC_PORT}`);
  const conf = page.getByTestId('review-config');
  if (FED_ALLOW) {
    await expect(conf).toContainText(/federation:\s+mode: allowlist/);
    for (const d of FED_ALLOW.split(',')) await expect(conf).toContainText(d.trim());
  } else {
    await expect(conf).toContainText(/federation:\s+mode: directory/);
  }
  await shot(page, 'review', P);

  // ---- install ----
  await page.getByTestId('install').click();
  const doneOrError = page.getByRole('heading', { name: 'Boson is running' }).or(page.getByTestId('install-error'));
  await expect(doneOrError.first()).toBeVisible({ timeout: 25 * 60_000 });
  if (DEV && (await page.getByTestId('install-error').isVisible())) {
    // The dev server's fake compose fails the first start once.
    await page.getByTestId('retry').click();
    await expect(page.getByRole('heading', { name: 'Boson is running' })).toBeVisible({ timeout: 60_000 });
  }
  if (await page.getByTestId('install-error').isVisible()) throw new Error('install failed: ' + (await page.getByTestId('install-error').innerText()));
  await expect(page.getByTestId('done-irc')).toContainText(`irc.${DOMAIN}:6697`);
  await expect(page.getByTestId('oper-password')).toHaveCount(0);
  await shot(page, 'done', P);
  if (!DEV) {
    await page.getByTestId('finish').click();
    await expect(page.getByText('Setup is switched off.')).toBeVisible();
  }
  expect(errors).toEqual([]);
});
