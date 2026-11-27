import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Dev: `go run ./cmd/devserver` serves the API on :8081; vite proxies /api to it.
export default defineConfig({
	plugins: [svelte()],
	build: { outDir: 'dist', emptyOutDir: false, assetsDir: 'assets' },
	server: { proxy: { '/api': 'http://127.0.0.1:8081' } },
	test: { include: ['src/**/*.test.js'] }
});
