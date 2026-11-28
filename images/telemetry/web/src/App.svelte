<script>
	import { api, auth } from './lib/api.svelte.js';
	import { route, navigate } from './lib/router.svelte.js';
	import Logo from './lib/Logo.svelte';
	import Login from './Login.svelte';
	import Overview from './pages/Overview.svelte';
	import Versions from './pages/Versions.svelte';
	import Features from './pages/Features.svelte';
	import Hardware from './pages/Hardware.svelte';
	import Problems from './pages/Problems.svelte';
	import Install from './pages/Install.svelte';
	import Apps from './pages/Apps.svelte';
	import Countries from './pages/Countries.svelte';

	const nav = [
		{ href: '/', label: 'Overview' },
		{ href: '/versions', label: 'Versions' },
		{ href: '/features', label: 'Features' },
		{ href: '/hardware', label: 'Hardware' },
		{ href: '/problems', label: 'Problems' },
		{ href: '/apps', label: 'Apps' },
		{ href: '/countries', label: 'Countries' }
	];
	let menuOpen = $state(false);
	let theme = $state('system');

	$effect(() => {
		api('GET', '/me')
			.then((m) => (auth.user = m.username))
			.catch(() => (auth.user = null))
			.finally(() => (auth.checked = true));
	});
	$effect(() => {
		try {
			const t = localStorage.getItem('theme');
			if (t === 'dark' || t === 'light') theme = t;
		} catch {
			/* storage blocked */
		}
	});
	$effect(() => {
		if (theme === 'system') document.documentElement.removeAttribute('data-theme');
		else document.documentElement.dataset.theme = theme;
	});
	function cycleTheme() {
		theme = theme === 'system' ? 'dark' : theme === 'dark' ? 'light' : 'system';
		try {
			if (theme === 'system') localStorage.removeItem('theme');
			else localStorage.setItem('theme', theme);
		} catch {
			/* storage blocked */
		}
	}
	$effect(() => {
		void route.path;
		menuOpen = false;
	});
	const active = (href) => (href === '/' ? route.path === '/' : route.path === href || route.path.startsWith(href + '/'));
	let installID = $derived(route.path.startsWith('/installs/') ? route.path.slice(10) : '');

	async function signOut() {
		try {
			await api('POST', '/logout');
		} catch {
			/* gone anyway */
		}
		auth.user = null;
		navigate('/', true);
	}
</script>

{#if !auth.checked}
	<p class="boot muted">Loading…</p>
{:else if !auth.user}
	<Login />
{:else}
	<a class="skip" href="#main">Skip to content</a>
	<div class="shell">
		<header class="rail">
			<div class="top">
				<a class="brand" href="/" aria-label="Boson stats, overview">
					<span class="mark"><Logo /></span>
					<span class="word">Boson</span>
					<span class="sub">stats</span>
				</a>
				<button class="menu btn btn-small" aria-expanded={menuOpen} aria-controls="nav" onclick={() => (menuOpen = !menuOpen)}
					>{menuOpen ? 'Close' : 'Menu'}</button
				>
			</div>
			<nav id="nav" class:open={menuOpen} aria-label="Main">
				<div class="links">
					<div class="group" id="nav-stats">Usage &amp; health</div>
					<ul aria-labelledby="nav-stats">
						{#each nav as item (item.href)}
							<li><a href={item.href} aria-current={active(item.href) || (item.href === '/problems' && installID) ? 'page' : undefined}>{item.label}</a></li>
						{/each}
					</ul>
				</div>
				<div class="foot">
					<div class="who"><span class="label">Signed in as</span><span class="me">{auth.user}</span></div>
					<div class="row">
						<button class="btn btn-small" onclick={cycleTheme}>Theme: {theme}</button>
						<button class="btn btn-small" onclick={signOut}>Sign out</button>
					</div>
					<span class="attr">IP to country: <a href="https://db-ip.com" rel="noopener">IP Geolocation by DB-IP</a>, <a href="https://creativecommons.org/licenses/by/4.0/" rel="noopener">CC BY 4.0</a></span>
				</div>
			</nav>
		</header>
		<main id="main" tabindex="-1">
			{#if route.path === '/'}<Overview />
			{:else if route.path === '/versions'}<Versions />
			{:else if route.path === '/features'}<Features />
			{:else if route.path === '/hardware'}<Hardware />
			{:else if route.path === '/problems'}<Problems />
			{:else if installID}<Install id={installID} />
			{:else if route.path === '/apps'}<Apps />
			{:else if route.path === '/countries'}<Countries />
			{:else}
				<h1>Not found</h1>
				<p class="muted">No page at {route.path}. <a href="/">Overview</a></p>
			{/if}
		</main>
	</div>
{/if}

<style>
	.boot {
		padding: var(--space-10);
	}
	.skip {
		position: absolute;
		left: -9999px;
	}
	.skip:focus {
		left: var(--space-4);
		top: var(--space-4);
		z-index: 100;
		background: var(--surface-raised);
		color: var(--fg);
		padding: var(--space-2) var(--space-3);
	}
	.shell {
		display: grid;
		grid-template-columns: var(--rail) minmax(0, 1fr);
		min-height: 100vh;
		background: linear-gradient(90deg, var(--surface) calc(var(--rail) - 1px), var(--border) calc(var(--rail) - 1px) var(--rail), transparent var(--rail));
	}
	.rail {
		position: sticky;
		top: 0;
		height: 100vh;
		height: 100dvh;
		display: flex;
		flex-direction: column;
		background: var(--surface);
		border-right: 1px solid var(--border);
		padding: var(--space-5) var(--space-4) var(--space-4);
	}
	.top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-2);
		color: var(--fg);
		text-decoration: none;
		padding: var(--space-1) var(--space-3);
		margin-left: calc(-1 * var(--space-1));
	}
	.mark {
		display: inline-flex;
		color: var(--accent);
	}
	.word {
		font-family: var(--font-display);
		font-size: 17px;
		font-weight: 600;
	}
	.sub {
		font-family: var(--font-mono);
		font-size: var(--text-sm);
		color: var(--fg-quiet);
	}
	.menu {
		display: none;
	}
	nav {
		display: flex;
		flex-direction: column;
		flex: 1 1 auto;
		min-height: 0;
		margin-top: var(--space-6);
	}
	.group {
		font-family: var(--font-mono);
		font-size: var(--text-xs);
		font-weight: 600;
		color: var(--fg-quiet);
		letter-spacing: var(--tracking-label);
		text-transform: uppercase;
		padding: 0 var(--space-3) var(--space-2);
	}
	nav ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 2px;
	}
	nav a {
		display: flex;
		padding: var(--space-2) var(--space-3);
		border-radius: var(--radius-sm);
		border-left: 2px solid transparent;
		color: var(--fg-2);
		text-decoration: none;
		font-family: var(--font-display);
		font-size: var(--text-sm);
		font-weight: 600;
	}
	nav a:hover {
		color: var(--fg);
		background: var(--surface-hover);
	}
	nav a[aria-current='page'] {
		color: var(--accent-text);
		background: var(--surface-raised);
		border-left-color: var(--accent);
		border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
	}
	.links {
		flex: 1 1 auto;
		min-height: 0;
		overflow-y: auto;
	}
	.foot {
		flex: none;
		margin-top: auto;
		display: grid;
		gap: var(--space-3);
		padding: var(--space-4) var(--space-3) 0;
		border-top: 1px solid var(--border-soft);
		font-size: var(--text-sm);
	}
	.who {
		display: grid;
		gap: 2px;
	}
	.label,
	.attr {
		font-family: var(--font-mono);
		font-size: var(--text-xs);
		color: var(--fg-quiet);
	}
	.attr a {
		display: inline;
		padding: 0;
		border: 0;
		font: inherit;
		color: var(--fg-2);
		text-decoration: underline;
	}
	.me {
		font-family: var(--font-mono);
		font-weight: 600;
		overflow-wrap: anywhere;
	}
	.row {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-2);
	}
	main {
		padding: var(--space-10) clamp(16px, 4vw, 56px) var(--space-12);
		max-width: 1280px;
		width: 100%;
		outline: none;
	}
	@media (max-width: 820px) {
		.shell {
			grid-template-columns: minmax(0, 1fr);
			grid-template-rows: auto 1fr;
			background: none;
		}
		.rail {
			height: auto;
			z-index: 20;
			padding: var(--space-3) var(--space-4);
			border-right: 0;
			border-bottom: 1px solid var(--border);
			background: color-mix(in oklab, var(--surface) 92%, transparent);
			backdrop-filter: blur(12px);
		}
		.menu {
			display: inline-flex;
		}
		nav {
			display: none;
			margin-top: var(--space-4);
		}
		nav.open {
			display: flex;
			max-height: calc(100dvh - 72px);
		}
		.foot {
			margin-top: var(--space-4);
			padding-bottom: var(--space-2);
		}
		main {
			padding: var(--space-6) 16px var(--space-12);
		}
	}
</style>
