<script>
	/** @type {{ label: string, value: string|number, sub?: string, tone?: string }} */
	let { label, value, sub = '', tone = '' } = $props();
</script>

<div class="stat {tone}">
	<span class="label">{label}</span>
	<span class="value">{value}</span>
	{#if sub}<span class="sub">{sub}</span>{/if}
</div>

<style>
	.stat {
		display: grid;
		gap: 2px;
		padding: var(--space-4);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		min-width: 0;
	}
	.label {
		font-family: var(--font-mono);
		font-size: var(--text-xs);
		letter-spacing: var(--tracking-label);
		text-transform: uppercase;
		color: var(--fg-quiet);
	}
	.value {
		font-family: var(--font-display);
		font-size: var(--text-3xl);
		font-weight: 700;
		letter-spacing: -0.02em;
		font-variant-numeric: tabular-nums;
		line-height: 1.1;
	}
	.sub {
		font-size: var(--text-sm);
		color: var(--fg-quiet);
	}
	.bad .value {
		color: var(--danger-text);
	}
</style>
