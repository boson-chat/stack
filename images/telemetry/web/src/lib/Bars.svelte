<script>
	// A ranked list of counts with a thin bar each (share of `total`).
	import { num, pct, label as nice } from './fmt.js';

	/** @type {{ items: {name: string, count: number}[], total: number, title?: string, empty?: string, fmt?: (n: string) => string }} */
	let { items = [], total = 0, title = '', empty = 'None yet.', fmt = nice } = $props();
	let max = $derived(Math.max(1, ...items.map((i) => i.count)));
</script>

<div class="bars">
	{#if title}<h3>{title}</h3>{/if}
	{#if !items.length}
		<p class="muted">{empty}</p>
	{:else}
		<ul>
			{#each items as it (it.name)}
				<li title="{fmt(it.name)}: {num(it.count)} of {num(total)}">
					<span class="name">{fmt(it.name)}</span>
					<span class="val">{num(it.count)} <span class="muted">{pct(it.count, total)}</span></span>
					<span class="track"><span class="fill" style:width="{(100 * it.count) / max}%"></span></span>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.bars {
		padding: var(--space-4);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		min-width: 0;
	}
	h3 {
		font-size: var(--text-sm);
		margin-bottom: var(--space-3);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: var(--space-3);
	}
	li {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		gap: 4px var(--space-3);
		font-size: var(--text-sm);
	}
	.name {
		font-family: var(--font-mono);
		overflow-wrap: anywhere;
	}
	.val {
		font-family: var(--font-mono);
		font-variant-numeric: tabular-nums;
		text-align: right;
	}
	.track {
		grid-column: 1 / -1;
		height: 4px;
		background: var(--surface-hover);
		border-radius: 2px;
		overflow: hidden;
	}
	.fill {
		display: block;
		height: 100%;
		background: var(--series-1);
		border-radius: 2px;
	}
	.muted {
		font-size: var(--text-sm);
	}
</style>
