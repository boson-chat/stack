<script>
	import { api, auth } from './lib/api.svelte.js';
	import Logo from './lib/Logo.svelte';

	let username = $state('');
	let password = $state('');
	let error = $state('');
	let busy = $state(false);

	async function submit(e) {
		e.preventDefault();
		busy = true;
		error = '';
		try {
			const r = await api('POST', '/login', { username, password });
			password = '';
			auth.user = r.username;
		} catch (err) {
			error = err.message;
		} finally {
			busy = false;
		}
	}
</script>

<main class="login">
	<form onsubmit={submit} aria-labelledby="login-h">
		<div class="brand"><span class="mark"><Logo size={22} /></span><span class="word">Boson</span><span class="sub">stats</span></div>
		<h1 id="login-h">Sign in</h1>
		<p class="muted">Anonymous usage and health stats from Boson installs and apps.</p>
		<label class="field">
			<span>Username</span>
			<input type="text" autocomplete="username" bind:value={username} required />
		</label>
		<label class="field">
			<span>Password</span>
			<input type="password" autocomplete="current-password" bind:value={password} required />
		</label>
		{#if error}<p class="err" role="alert">{error}</p>{/if}
		<button class="btn btn-primary" type="submit" disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</button>
	</form>
</main>

<style>
	.login {
		min-height: 100vh;
		display: grid;
		place-items: center;
		padding: var(--space-6) 16px;
	}
	form {
		width: 100%;
		max-width: 360px;
		display: grid;
		gap: var(--space-4);
		padding: var(--space-8) var(--space-6);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-md);
	}
	.brand {
		display: flex;
		align-items: center;
		gap: var(--space-2);
	}
	.mark {
		color: var(--accent);
		display: inline-flex;
	}
	.word {
		font-family: var(--font-display);
		font-size: 18px;
		font-weight: 600;
	}
	.sub {
		font-family: var(--font-mono);
		color: var(--fg-quiet);
	}
	.muted {
		font-size: var(--text-sm);
	}
	.err {
		color: var(--danger-text);
		font-size: var(--text-sm);
	}
	input {
		width: 100%;
	}
</style>
