<script>
	import Load from '../lib/Load.svelte';
	import LineChart from '../lib/LineChart.svelte';
	import { num, pct, country, flag } from '../lib/fmt.js';
	import { route, navigate } from '../lib/router.svelte.js';

	let behind = $derived(Number(new URLSearchParams(route.search).get('behind')) || 3);
	function setBehind(e) {
		const n = Math.min(50, Math.max(1, Number(e.currentTarget.value) || 3));
		navigate('/versions?behind=' + n, true);
	}
</script>

<div class="page-head">
	<div>
		<h1>Versions</h1>
		<p>What installs active in the last 7 days run: the stack release and each service's image.</p>
	</div>
</div>

<Load path="/versions?behind={behind}">
	{#snippet children(d)}
		<LineChart title="Stack releases, installs per day" series={d.series} by="version" error={d.series_error} />

		<section class="section" aria-labelledby="h-old">
			<div class="section-head">
				<h2 id="h-old">Behind the newest release</h2>
				<label class="behind">
					<span>At least</span>
					<input type="number" min="1" max="50" value={d.behind} onchange={setBehind} aria-label="Releases behind" />
					<span>releases behind {d.latest ? 'v' + d.latest : ''}</span>
				</label>
			</div>
			{#if !d.old?.length}
				<p class="muted">No active install is {d.behind} or more releases behind (of {d.releases.length} releases seen).</p>
			{:else}
				<p class="muted lead">{num(d.old.length)} of {num(d.installs)} active installs ({pct(d.old.length, d.installs)}).</p>
				<div class="table-wrap">
					<table>
						<thead><tr><th>Install</th><th>Stack</th><th class="num">Behind</th><th>Country</th></tr></thead>
						<tbody>
							{#each d.old as o (o.id)}
								<tr>
									<td><a class="mono" href="/installs/{o.id}">{o.id}</a></td>
									<td class="mono">{o.version}</td>
									<td class="num">{o.behind}</td>
									<td>{flag(o.country)} {country(o.country)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>

		<section class="section" aria-labelledby="h-svc">
			<div class="section-head"><h2 id="h-svc">Per service</h2><p>Share of the installs that run that service.</p></div>
			<div class="grid">
				{#each d.services as s (s.service)}
					<div class="card">
						<h3>{s.service} <span class="muted">{num(s.installs)}</span></h3>
						<table>
							<tbody>
								{#each s.versions.slice(0, 8) as v (v.version)}
									<tr>
										<td class="mono">{v.version}</td>
										<td class="num">{num(v.count)}</td>
										<td class="num muted">{pct(v.count, s.installs)}</td>
									</tr>
								{/each}
								{#if s.versions.length > 8}
									<tr><td colspan="3" class="muted">+ {s.versions.length - 8} older</td></tr>
								{/if}
							</tbody>
						</table>
					</div>
				{/each}
			</div>
		</section>
	{/snippet}
</Load>

<style>
	.behind {
		display: inline-flex;
		align-items: center;
		gap: var(--space-2);
		font-size: var(--text-sm);
		color: var(--fg-2);
		flex-wrap: wrap;
	}
	.behind input {
		width: 64px;
	}
	.lead {
		margin-bottom: var(--space-3);
		font-size: var(--text-sm);
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
		gap: var(--space-3);
	}
	.card {
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		padding: var(--space-3) 0 var(--space-1);
		min-width: 0;
	}
	.card h3 {
		font-family: var(--font-mono);
		padding: 0 var(--space-4) var(--space-2);
	}
	.card td {
		padding: var(--space-1) var(--space-4);
	}
</style>
