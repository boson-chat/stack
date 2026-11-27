import { defineConfig, devices } from '@playwright/test';

// The panel against cmd/devserver (made-up data, an in-memory VictoriaMetrics
// fake unless E2E_VM_URL names a real one). SHOTS=dir saves screenshots.
const vm = process.env.E2E_VM_URL ? ` -vm ${process.env.E2E_VM_URL}` : '';
export default defineConfig({
	testDir: 'e2e',
	timeout: 30_000,
	retries: process.env.CI ? 1 : 0,
	use: { baseURL: 'http://127.0.0.1:18081', trace: 'retain-on-failure' },
	projects: [
		{ name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 900 } } },
		{ name: 'phone', use: { ...devices['Pixel 7'], viewport: { width: 390, height: 844 } } }
	],
	webServer: {
		command: `go run ./cmd/devserver -panel 127.0.0.1:18081 -ingest 127.0.0.1:18080${vm}`,
		cwd: '..',
		url: 'http://127.0.0.1:18081/healthz',
		timeout: 180_000,
		reuseExistingServer: !process.env.CI
	}
});
