<script>
	import Load from '../lib/Load.svelte';
	import Stat from '../lib/Stat.svelte';
	import LineChart from '../lib/LineChart.svelte';
	import { num, range, day } from '../lib/fmt.js';

	const people = [
		['accounts', 'Accounts'],
		['active_30d', 'Active, 30 days'],
		['active_7d', 'Active, 7 days'],
		['irc_peak_24h', 'Peak on IRC, 24 h']
	];
</script>

<div class="page-head">
	<div>
		<h1>Overview</h1>
		<p>Installs that sent a report, and the people on them, summed from each install's bucketed counts.</p>
	</div>
</div>

<Load path="/overview">
	{#snippet children(d)}
		<section aria-labelledby="h-active">
			<h2 id="h-active" class="sr-only">Active installs</h2>
			<div class="tiles">
				<Stat label="Active installs, 1 day" value={num(d.active.d1)} />
				<Stat label="Active, 7 days" value={num(d.active.d7)} />
				<Stat label="Active, 30 days" value={num(d.active.d30)} sub="{num(d.total)} ever seen" />
				<Stat label="App devices, 1 day" value={num(d.devices.d1)} sub="{num(d.devices.d7)} in 7 days" />
			</div>
		</section>

		<section class="section" aria-labelledby="h-people">
			<div class="section-head">
				<h2 id="h-people">People across installs</h2>
				<p>From the latest report of each install active in the last 7 days. Counts arrive in buckets, so these are ranges.</p>
			</div>
			<div class="tiles">
				{#each people as [k, l] (k)}
					{@const r = range(d.people[k])}
					<Stat label={l} value={r.main} sub={r.sub} />
				{/each}
			</div>
		</section>

		<section class="section" aria-labelledby="h-trend">
			<div class="section-head"><h2 id="h-trend">Daily</h2><p>One point per finished UTC day.</p></div>
			<div class="two">
				<LineChart title="Installs that reported" series={d.series.installs} error={d.series_error} />
				<LineChart title="App devices that pinged" series={d.series.devices} error={d.series_error} />
			</div>
		</section>

		<section class="section" aria-labelledby="h-weeks">
			<div class="section-head">
				<h2 id="h-weeks">New and stopped, per week</h2>
				<p>Stopped: installs whose last report fell in that week and that have been silent for over 7 days since.</p>
			</div>
			<div class="table-wrap">
				<table>
					<thead><tr><th>Week of</th><th class="num">New</th><th class="num">Stopped reporting</th></tr></thead>
					<tbody>
						{#each [...d.weeks].reverse() as w (w.week)}
							<tr><td>{day(w.week)}</td><td class="num">{num(w.new)}</td><td class="num">{num(w.stopped)}</td></tr>
						{/each}
					</tbody>
				</table>
			</div>
		</section>
	{/snippet}
</Load>

<style>
	.tiles {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: var(--space-3);
	}
	.two {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
		gap: var(--space-3);
	}
</style>
