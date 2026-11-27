<script>
	import Load from '../lib/Load.svelte';
	import { ago, country, day, flag, label } from '../lib/fmt.js';

	let { id } = $props();
	let open = $state('');
	const tone = (k) => (['update_failed', 'crash_loop', 'unhealthy', 'disk'].includes(k) ? 'tag-bad' : 'tag-warn');
	const p1 = (v) => (v == null ? '–' : v.toFixed(0) + '%');
	const sick = (r) => Object.entries(r.health.services || {}).filter(([, s]) => s.state !== 'running' || s.restarts > 0);
</script>

<p class="crumb"><a href="/problems">Problems</a> / <span class="mono">{id}</span></p>

<Load path="/installs/{id}">
	{#snippet children(d)}
		<div class="page-head">
			<div>
				<h1 class="mono">{d.id}</h1>
				<p>
					{flag(d.country)} {country(d.country)} · stack <span class="mono">{d.stack}</span> · first seen {day(d.first_seen)} · last report {ago(d.last_seen)}
				</p>
			</div>
		</div>
		<h2>Report history</h2>
		<p class="muted sub">One row per UTC day, the last 90 days.</p>
		<div class="table-wrap">
			<table>
				<thead><tr><th>Day</th><th>Problems</th><th>Updates</th><th>Services not fine</th><th class="num">CPU / mem / disk</th><th></th></tr></thead>
				<tbody>
					{#each d.history as h (h.day)}
						{@const u = h.report.health.updates}
						<tr>
							<td class="mono">{h.day}</td>
							<td><span class="tags">{#each h.problems.kinds as k (k)}<span class="tag {tone(k)}">{label(k)}</span>{:else}<span class="tag tag-ok">OK</span>{/each}</span></td>
							<td class="mono">{u.attempts ? `${u.attempts} tried, ${u.failures} failed, ${u.rollbacks} rolled back` : '–'}{u.errors.length ? ` (${u.errors.join(', ')})` : ''}</td>
							<td class="mono">{sick(h.report).map(([n, s]) => `${n} ${s.state}${s.restarts ? ' ×' + s.restarts : ''}`).join(', ') || '–'}</td>
							<td class="num">{h.report.health.host ? `${p1(h.report.health.host.cpu_p95)} / ${p1(h.report.health.host.mem_p95)} / ${p1(h.report.health.host.disk_used)}` : '–'}</td>
							<td><button class="btn btn-small" aria-expanded={open === h.day} onclick={() => (open = open === h.day ? '' : h.day)}>{open === h.day ? 'Hide' : 'Report'}</button></td>
						</tr>
						{#if open === h.day}
							<tr class="raw"><td colspan="6"><pre>{JSON.stringify(h.report, null, 2)}</pre></td></tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/snippet}
</Load>

<style>
	.crumb {
		font-size: var(--text-sm);
		color: var(--fg-quiet);
		margin-bottom: var(--space-4);
	}
	h1 {
		overflow-wrap: anywhere;
	}
	.sub {
		font-size: var(--text-sm);
		margin: var(--space-1) 0 var(--space-3);
	}
	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	td {
		vertical-align: top;
	}
	pre {
		margin: 0;
		font-family: var(--font-mono);
		font-size: var(--text-xs);
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		max-height: 420px;
		overflow: auto;
	}
	.raw:hover {
		background: none;
	}
</style>
