// The web profile end to end (docs/web.md): the web client loads from
// https://WEB_HOST with its headers, a guest reaches this stack's Ergo
// through webircgateway, and a signed-in user joins a voice channel over
// WebTransport. Runs after scripts/smoke-irc.py, which leaves a voice token
// for its user in ../../voice-token ($VOICE_TOKEN).
import { test, expect, type Page } from '@playwright/test';

const WEB = `https://${process.env['WEB_HOST'] ?? 'chat.localhost'}`;
const IRC_HOST = process.env['IRC_HOST'] ?? 'irc.localhost';
const AUTH_HOST = process.env['AUTH_HOST'] ?? 'auth.localhost';

// A form field by its label (the app's design system: .bds-field).
function field(scope: ReturnType<Page['getByRole']>, label: string) {
  return scope.locator('.bds-field').filter({ has: scope.page().locator('label.bds-field-label', { hasText: new RegExp(`^\\s*${label}`) }) }).locator('input').first();
}

async function open(page: Page): Promise<void> {
  const res = await page.goto(WEB, { waitUntil: 'domcontentloaded' });
  expect(res?.status()).toBe(200);
  await page.waitForFunction(() => 'bosonShellReady' in window, null, { timeout: 30_000 });
}

test('the page loads with its CSP and this stack\'s endpoints', async ({ page, request }) => {
  const res = await request.get(WEB);
  expect(res.status()).toBe(200);
  const csp = res.headers()['content-security-policy'] ?? '';
  expect(csp).toContain("script-src 'self' 'wasm-unsafe-eval'");
  expect(csp).toContain("frame-ancestors 'none'");
  const cfg = await (await request.get(`${WEB}/boson-config.js`)).text();
  expect(cfg).toContain(`"https://${AUTH_HOST}"`);
  await open(page);
  await expect(page.getByRole('button', { name: 'Continue without an account' })).toBeVisible();
});

test('a guest connects to this network\'s Ergo through the gateway and joins a channel', async ({ page }) => {
  const errors: string[] = [];
  page.on('pageerror', (e) => errors.push(e.message));
  await open(page);
  const nick = `web${Math.random().toString(36).slice(2, 8)}`;
  await page.getByRole('button', { name: 'Continue without an account' }).click();
  await page.getByPlaceholder('your-nick').fill(nick);
  await page.getByRole('button', { name: 'Continue', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Server Directory' })).toBeVisible();

  await page.getByRole('button', { name: 'Advanced', exact: true }).click();
  await page.getByRole('button', { name: '+ Add server manually' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add a server manually' });
  await field(dialog, 'Hostname').fill(IRC_HOST);
  await field(dialog, 'Port').fill('6697');
  await field(dialog, 'Display name').fill('Local stack');
  const tls = dialog.getByRole('checkbox', { name: 'Use TLS (recommended)' });
  if (!(await tls.isChecked())) await dialog.getByText('Use TLS (recommended)').click();
  await dialog.getByRole('button', { name: /^Add/ }).click();
  await page.locator('article.directory-card').filter({ hasText: 'Local stack' }).getByRole('button', { name: 'Connect' }).click();
  await expect(page.locator('.sidebar-status-dot')).toHaveAttribute('data-state', 'connected', { timeout: 30_000 });

  const channel = `#web-${Math.random().toString(36).slice(2, 8)}`;
  await page.getByRole('button', { name: 'Add channel' }).click();
  const input = page.getByPlaceholder('Search channels by name or topic — or type #channel to join directly');
  await input.fill(channel);
  await input.press('Enter');
  await expect(page.getByPlaceholder(new RegExp(`^Message #?${channel.slice(1)}$`))).toBeVisible();
  expect(errors).toEqual([]);
});

test('voice: joining a voice channel over WebTransport', async ({ page }) => {
  const token = process.env['VOICE_TOKEN'];
  test.skip(!token, 'no VOICE_TOKEN');
  await open(page);
  const state = await page.evaluate(async ({ host, token }) => {
    type Voice = {
      discover(h: string, r: boolean): Promise<{ available: boolean; audio: boolean; audioError?: string }>;
      join(r: { host: string; room: string; roomName: string; token: string }): Promise<unknown>;
      state(): Promise<{ status: string; error?: string; participants: { self: boolean }[] }>;
      leave(): Promise<void>;
    };
    const v = (window as unknown as { bosonVoice?: Voice }).bosonVoice;
    if (!v) return { status: 'no bridge' };
    const info = await v.discover(host, true);
    if (!info.available || !info.audio) return { status: 'unavailable', error: JSON.stringify(info) };
    await v.join({ host, room: 'general', roomName: 'General', token: token! });
    for (let i = 0; i < 60; i++) {
      const s = await v.state();
      if (s.status === 'connected' || s.status === 'error') {
        await v.leave();
        return { status: s.status, error: s.error, self: s.participants.some((p) => p.self) };
      }
      await new Promise((r) => setTimeout(r, 250));
    }
    return { status: 'timeout' };
  }, { host: IRC_HOST, token });
  expect(state, JSON.stringify(state)).toMatchObject({ status: 'connected', self: true });
});
