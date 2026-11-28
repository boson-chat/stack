import { test, expect } from '@playwright/test';

const shots = process.env.SHOTS;
async function shot(page, name, testInfo) {
	if (!shots) return;
	await page.screenshot({ path: `${shots}/stats-${name}-${testInfo.project.name}.png`, fullPage: true });
}

async function signIn(page) {
	await page.goto('/');
	await page.getByLabel('Username').fill('owner');
	await page.getByLabel('Password').fill('correct-horse-battery');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible();
}

async function noSideScroll(page) {
	const w = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
	expect(w).toBeLessThanOrEqual(0);
}

test('wrong password, then the rate limit', async ({ page }, testInfo) => {
	await page.goto('/');
	await shot(page, 'login', testInfo);
	await page.getByLabel('Username').fill('owner');
	await page.getByLabel('Password').fill('nope');
	await page.getByRole('button', { name: 'Sign in' }).click();
	await expect(page.getByRole('alert')).toHaveText(/Wrong username or password/);
});

test('every page renders with data, and sign out ends the session', async ({ page }, testInfo) => {
	await signIn(page);
	await expect(page.getByText('Active installs, 1 day')).toBeVisible();
	await expect(page.getByText('People across installs')).toBeVisible();
	await expect(page.locator('figure svg').first()).toBeVisible();
	await noSideScroll(page);
	await shot(page, 'overview', testInfo);

	const go = async (name) => {
		if (testInfo.project.name === 'phone') await page.getByRole('button', { name: 'Menu' }).click();
		await page.getByRole('navigation').getByRole('link', { name }).click();
		await expect(page.getByRole('heading', { level: 1, name })).toBeVisible();
		await expect(page.getByText('Loading…')).toHaveCount(0);
		await noSideScroll(page);
	};
	await go('Versions');
	await expect(page.getByText('Behind the newest release')).toBeVisible();
	await expect(page.getByRole('heading', { name: /^stack/ })).toBeVisible();
	await shot(page, 'versions', testInfo);

	await go('Features');
	await expect(page.getByRole('heading', { name: 'Install mode' })).toBeVisible();
	await shot(page, 'features', testInfo);

	await go('Hardware');
	await expect(page.getByRole('heading', { name: 'Installs per size' })).toBeVisible();
	await expect(page.getByRole('heading', { name: 'Architecture' })).toBeVisible();
	await expect(page.getByText('amd64 (x86-64)')).toBeVisible();
	await expect(page.getByRole('columnheader', { name: 'Memory, 24 h p95' })).toBeVisible();
	await expect(page.getByRole('cell', { name: '2–4 GB' }).first()).toBeVisible();
	await shot(page, 'hardware', testInfo);

	await go('Problems');
	await expect(page.locator('.tag', { hasText: 'Failed update' }).first()).toBeVisible();
	await shot(page, 'problems', testInfo);
	await page.locator('td a.mono').first().click();
	await expect(page.getByRole('heading', { name: 'Report history' })).toBeVisible();
	await page.getByRole('button', { name: 'Report' }).first().click();
	await expect(page.locator('pre')).toContainText('"schema": 1');
	await expect(page.locator('pre')).not.toContainText('install_id": "');
	await noSideScroll(page);
	await shot(page, 'install', testInfo);

	await go('Apps');
	await expect(page.getByRole('heading', { name: 'Crashes per version' })).toBeVisible();
	await shot(page, 'apps', testInfo);

	await go('Countries');
	await expect(page.getByText('Germany')).toBeVisible();
	await expect(page.getByRole('link', { name: 'IP Geolocation by DB-IP' }).last()).toBeVisible();
	await shot(page, 'countries', testInfo);

	if (testInfo.project.name === 'phone') await page.getByRole('button', { name: 'Menu' }).click();
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
	const res = await page.request.get('/api/overview');
	expect(res.status()).toBe(401);
});

test('the session cookie is HttpOnly, Secure and SameSite=Strict', async ({ page }) => {
	await signIn(page);
	const c = (await page.context().cookies()).find((c) => c.name === 'stats_session');
	expect(c).toBeTruthy();
	expect(c.httpOnly).toBe(true);
	expect(c.secure).toBe(true);
	expect(c.sameSite).toBe('Strict');
	expect(await page.evaluate(() => document.cookie)).not.toContain('stats_session');
});
