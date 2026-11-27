<script>
	import Load from '../lib/Load.svelte';
	import { num, pct, country, flag } from '../lib/fmt.js';
</script>

<div class="page-head">
	<div>
		<h1>Countries</h1>
		<p>Where installs and app devices active in the last 30 days are, from the country of the address each report came from. The address itself is never stored.</p>
	</div>
</div>

<Load path="/countries">
	{#snippet children(d)}
		<div class="table-wrap">
			<table>
				<thead><tr><th>Country</th><th class="num">Installs</th><th class="num">Share</th><th class="num">App devices</th><th class="num">Share</th></tr></thead>
				<tbody>
					{#each d.countries as c (c.country)}
						<tr>
							<td>{flag(c.country)} {country(c.country)} <span class="muted mono">{c.country}</span></td>
							<td class="num">{num(c.installs)}</td>
							<td class="num muted">{pct(c.installs, d.installs)}</td>
							<td class="num">{num(c.devices)}</td>
							<td class="num muted">{pct(c.devices, d.devices)}</td>
						</tr>
					{:else}
						<tr><td colspan="5" class="muted">Nothing reported in the last 30 days.</td></tr>
					{/each}
				</tbody>
			</table>
		</div>
		<p class="muted attr">IP to country data: <a href="https://db-ip.com" rel="noopener">IP Geolocation by DB-IP</a>, licensed under <a href="https://creativecommons.org/licenses/by/4.0/" rel="noopener">CC BY 4.0</a>.</p>
	{/snippet}
</Load>

<style>
	.attr {
		margin-top: var(--space-3);
		font-size: var(--text-sm);
	}
</style>
