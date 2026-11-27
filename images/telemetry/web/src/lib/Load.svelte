<script>
	// Loads `path` from the API and renders children(data); a reload on change.
	import { api } from './api.svelte.js';

	let { path, children } = $props();
	let data = $state(null);
	let error = $state('');

	$effect(() => {
		const p = path;
		let live = true;
		error = '';
		api('GET', p)
			.then((d) => live && (data = d))
			.catch((e) => live && (error = e.message));
		return () => (live = false);
	});
</script>

{#if error}
	<p class="err" role="alert">Couldn't load this page: {error}</p>
{:else if !data}
	<p class="muted" aria-busy="true">Loading…</p>
{:else}
	{@render children(data)}
{/if}

<style>
	.err {
		color: var(--danger-text);
	}
</style>
