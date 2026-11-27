<script>
	import Load from '../lib/Load.svelte';
	import LineChart from '../lib/LineChart.svelte';
	import { ago, country, flag, label } from '../lib/fmt.js';

	const tone = (k) => (['update_failed', 'crash_loop', 'unhealthy', 'disk'].includes(k) ? 'tag-bad' : 'tag-warn');
	const p1 = (v) => (v == null ? '–' : v.toFixed(0) + '%');
</script>

<div class="page-head">
	<div>
		<h1>Problems</h1>
		<p>Installs whose reports in the last 7 days show a failed update, a rollback, a crash-looping or unhealthy service, or a host under strain. IDs are anonymous: a short hash of the random install ID.</p>
	</div>
</div>

<Load path="/problems">
	{#snippet children(d)}
		<div class="kinds">
			{#each d.kinds as k (k.name)}
				<div class="kind" class:none={!k.count}><span class="n">{k.count}</span><span class="l">{label(k.name)}</span></div>
			{/each}
		</div>
		<p class="muted thr">
			Thresholds: CPU p95 &gt; {d.thresholds.cpu_p95}%, memory p95 &gt; {d.thresholds.mem_p95}%, disk &gt; {d.thresholds.disk_used}%, crash loop = restarting, or {d.thresholds.restart_growth}+ restarts since the previous report.
		</p>

		<section class="section" aria-labelledby="h-list">
			<div class="section-head"><h2 id="h-list">Installs</h2></div>
			{#if !d.installs.length}
				<p class="muted">No problems reported in the last 7 days.</p>
			{:else}
				<div class="table-wrap">
					<table>
						<thead>
							<tr><th>Install</th><th>Problems</th><th>Details</th><th class="num">CPU / mem / disk</th><th>Stack</th><th>Last report</th></tr>
						</thead>
						<tbody>
							{#each d.installs as r (r.id)}
								<tr>
									<td><a class="mono" href="/installs/{r.id}">{r.id}</a><br /><span class="muted">{flag(r.country)} {country(r.country)}</span></td>
									<td><span class="tags">{#each r.problems.kinds as k (k)}<span class="tag {tone(k)}">{label(k)}</span>{/each}</span></td>
									<td class="det">
										{#if r.problems.errors?.length}<div>Update failed at: <span class="mono">{r.problems.errors.join(', ')}</span></div>{/if}
										{#if r.problems.looping?.length}<div>Restarting: <span class="mono">{r.problems.looping.join(', ')}</span></div>{/if}
										{#if r.problems.unhealthy?.length}<div>Unhealthy: <span class="mono">{r.problems.unhealthy.join(', ')}</span></div>{/if}
										{#if !r.healthy}<div class="muted">Last report: not healthy</div>{/if}
									</td>
									<td class="num">{r.host ? `${p1(r.host.cpu_p95)} / ${p1(r.host.mem_p95)} / ${p1(r.host.disk_used)}` : '–'}</td>
									<td class="mono">{r.stack}</td>
									<td>{ago(r.last_seen)}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}
		</section>

		<section class="section">
			<LineChart title="Installs with each problem, per day" series={d.series} by="problem" error={d.series_error} />
		</section>
	{/snippet}
</Load>

<style>
	.kinds {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
		gap: var(--space-2);
	}
	.kind {
		display: grid;
		padding: var(--space-3) var(--space-4);
		background: var(--surface);
		border: 1px solid var(--border);
		border-left: 2px solid var(--danger);
		border-radius: var(--radius-sm);
	}
	.kind.none {
		border-left-color: var(--border);
	}
	.n {
		font-family: var(--font-display);
		font-size: var(--text-2xl);
		font-weight: 700;
	}
	.kind.none .n {
		color: var(--fg-quiet);
	}
	.l {
		font-size: var(--text-sm);
		color: var(--fg-2);
	}
	.thr {
		margin-top: var(--space-3);
		font-size: var(--text-sm);
	}
	.tags {
		display: flex;
		flex-wrap: wrap;
		gap: 4px;
	}
	.det {
		font-size: var(--text-sm);
		min-width: 180px;
	}
	td {
		vertical-align: top;
	}
</style>
