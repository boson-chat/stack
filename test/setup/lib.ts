import { expect, type Page } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import { mkdirSync } from 'node:fs';
import path from 'node:path';

export const DEV = process.env['DEV'] === '1';
export const DOMAIN = process.env['SETUP_DOMAIN'] ?? 'boson.test';
export const BAD_DOMAIN = process.env['SETUP_BAD_DOMAIN'] ?? 'net.elsewhere.test';
export const ADMIN = process.env['SETUP_ADMIN'] ?? 'alice';
export const ADMIN_EMAIL = `${ADMIN}@${DOMAIN}`;
export const ADMIN_PASSWORD = process.env['SETUP_ADMIN_PASSWORD'] ?? 'correct horse battery';
const SHOTS = process.env['SHOTS_DIR'] ?? path.join(__dirname, 'shots');

let n = 0;
/** Screenshot the page at desktop and phone width. */
export async function shot(page: Page, name: string, prefix = ''): Promise<void> {
  if (process.env['SHOTS'] === '0') return;
  mkdirSync(SHOTS, { recursive: true });
  const i = String(++n).padStart(2, '0');
  const vp = page.viewportSize()!;
  await page.waitForTimeout(250);
  await page.screenshot({ path: path.join(SHOTS, `${prefix}${i}-${name}-desktop.png`), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.waitForTimeout(250);
  await page.screenshot({ path: path.join(SHOTS, `${prefix}${i}-${name}-phone.png`), fullPage: true });
  // No horizontal scroll at phone width.
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, `${name}: horizontal overflow at 390px`).toBeLessThanOrEqual(0);
  await page.setViewportSize(vp);
}

/** test/setup/hooks.sh NAME (CI only: firewall rules, a busy port). */
export function hook(name: string): void {
  if (DEV) return;
  execFileSync('bash', [path.join(__dirname, 'hooks.sh'), name], { stdio: 'inherit' });
}

export async function enterCode(page: Page, code: string): Promise<void> {
  await page.goto('/');
  await page.getByLabel('Setup code').fill(code);
  await page.getByRole('button', { name: 'Continue' }).click();
}

export const next = (page: Page) => page.getByTestId('next');
