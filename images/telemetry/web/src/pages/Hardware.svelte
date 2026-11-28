<script>
	import Load from '../lib/Load.svelte';
	import Bars from '../lib/Bars.svelte';
	import { num, pct, size } from '../lib/fmt.js';

	const titles = { cpu_cores: 'CPU cores', ram_gb: 'Memory', disk_gb: 'Disk', arch: 'Architecture' };
	// Each size next to the usage percentage it bounds.
	const use = [
		['cpu_cores', 'CPU cores', 'CPU, 24 h p95'],
		['ram_gb', 'Memory', 'Memory, 24 h p95'],
		['disk_gb', 'Disk', 'Disk used']
	];
	const pctText = (v) => (v == null ? '–' : v.toFixed(v < 10 ? 1 : 0) + '%');
</script>

<div class="page-head">
	<div>
		<h1>Hardware</h1>
		<p>
			The size of the servers that installs active in the last 7 days run on, in buckets. Unknown: an older admin panel
			that doesn't send it, or a value it couldn't read (disk needs the metrics profile).
		</p>
	</div>
</div>

<Load path="/hardware">
	{#snippet children(d)}
		<section aria-labelledby="h-dist">
			<div class="section-head">
				<h2 id="h-dist">Installs per size</h2>
				<p>{num(d.reported)} of {num(d.installs)} installs ({pct(d.reported, d.installs)}) sent their size.</p>
			</div>
			<div class="grid dist">
				{#each d.dims as dim (dim)}
					<Bars title={titles[dim]} items={d.sizes[dim]} total={d.installs} fmt={(b) => size(dim, b)} empty="No install reported it yet." />
				{/each}
			</div>
		</section>

		<section class="section" aria-labelledby="h-use">
			<div class="section-head">
				<h2 id="h-use">Size and use</h2>
				<p>The median of each install's latest usage reading, per size. Usage needs the metrics profile, so fewer installs report it.</p>
			</div>
			<div class="grid">
				{#each use as [dim, head, metric] (dim)}
					<div class="table-wrap">
						<table>
							<caption class="sr-only">{head}: median {metric}</caption>
							<thead><tr><th>{head}</th><th class="num">Installs</th><th class="num">{metric}</th></tr></thead>
							<tbody>
								{#each d.usage[dim] as u (u.bucket)}
									<tr>
										<td class="size">{size(dim, u.bucket)}</td>
										<td class="num">{num(u.installs)}</td>
										<td class="num" title="Median of {num(u.reported)} installs">
											<span class="meter" aria-hidden="true"><span style:width="{u.median ?? 0}%" class:hot={u.median >= 80}></span></span>
											{pctText(u.median)}
										</td>
									</tr>
								{:else}
									<tr><td colspan="3" class="muted">No install reported it yet.</td></tr>
								{/each}
							</tbody>
						</table>
					</div>
				{/each}
			</div>
		</section>
	{/snippet}
</Load>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: var(--space-3);
	}
	.dist {
		grid-template-columns: repeat(auto-fit, minmax(210px, 1fr));
	}
	.size {
		white-space: nowrap;
	}
	.meter {
		display: inline-block;
		vertical-align: middle;
		width: 44px;
		height: 4px;
		margin-right: var(--space-2);
		background: var(--surface-hover);
		border-radius: 2px;
		overflow: hidden;
	}
	.meter span {
		display: block;
		height: 100%;
		background: var(--series-1);
		border-radius: 2px;
	}
	.meter span.hot {
		background: var(--danger-text);
	}
</style>
