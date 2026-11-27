<script>
	import Load from '../lib/Load.svelte';
	import Bars from '../lib/Bars.svelte';
	import LineChart from '../lib/LineChart.svelte';
	import { num, label } from '../lib/fmt.js';
</script>

<div class="page-head">
	<div>
		<h1>Apps</h1>
		<p>Daily pings from the desktop, phone and web apps: one per device per day.</p>
	</div>
</div>

<Load path="/apps">
	{#snippet children(d)}
		{@const dau = d.platforms.reduce((a, p) => a + p.count, 0)}
		<LineChart title="Daily active devices by platform" series={d.series} by="platform" error={d.series_error} />
		<div class="grid section">
			<Bars title="Active today, by platform" items={d.platforms} total={dau} empty="No pings in the last 24 hours." />
			<Bars title="Servers connected per device" items={d.servers} total={d.servers.reduce((a, s) => a + s.count, 0)} fmt={(b) => b + (b === '1' ? ' server' : ' servers')} />
		</div>
		<section class="section" aria-labelledby="h-ver">
			<div class="section-head"><h2 id="h-ver">Versions</h2><p>Devices active in the last 1 and 7 days.</p></div>
			<div class="table-wrap">
				<table>
					<thead><tr><th>Platform</th><th>Version</th><th>Channel</th><th class="num">1 day</th><th class="num">7 days</th></tr></thead>
					<tbody>
						{#each d.versions as v (v.platform + v.version + v.channel)}
							<tr><td>{label(v.platform)}</td><td class="mono">{v.version}</td><td>{v.channel}</td><td class="num">{num(v.d1)}</td><td class="num">{num(v.d7)}</td></tr>
						{:else}
							<tr><td colspan="5" class="muted">No pings yet.</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
		<section class="section" aria-labelledby="h-cr">
			<div class="section-head"><h2 id="h-cr">Crashes per version</h2><p>Last 7 days: uncaught errors, renderer crashes and recovered panics the apps counted.</p></div>
			<div class="table-wrap">
				<table>
					<thead><tr><th>Platform</th><th>Version</th><th class="num">Crashes</th><th class="num">Devices</th><th class="num">Per device</th></tr></thead>
					<tbody>
						{#each d.crashes as c (c.platform + c.version)}
							<tr class:hot={c.per_device >= 1}>
								<td>{label(c.platform)}</td><td class="mono">{c.version}</td><td class="num">{num(c.crashes)}</td><td class="num">{num(c.devices)}</td><td class="num">{c.per_device.toFixed(2)}</td>
							</tr>
						{:else}
							<tr><td colspan="5" class="muted">No pings yet.</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	{/snippet}
</Load>

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: var(--space-3);
	}
	.hot td:nth-child(5) {
		color: var(--danger-text);
	}
</style>
