<script>
	import Load from '../lib/Load.svelte';
	import Bars from '../lib/Bars.svelte';
	import LineChart from '../lib/LineChart.svelte';
	import { num, pct, label } from '../lib/fmt.js';

	const sso = (k) => ({ oidc: 'OpenID Connect', saml: 'SAML', oauth2: 'OAuth 2', other: 'Other', none: 'None' })[k] ?? k;
	const mode = (k) => {
		const [m, i] = k.split(' · ');
		const ms = { fresh: 'Fresh (own Ergo)', addon: 'Add-on (bridge)', unknown: 'Unknown' }[m] ?? m;
		return i && i !== 'ergo' && i !== 'unknown' ? `${ms} · ${i}` : m === 'addon' && i === 'ergo' ? `${ms} · ergo` : ms;
	};
</script>

<div class="page-head">
	<div>
		<h1>Features</h1>
		<p>What installs active in the last 7 days have turned on and use. A feature counts as used when its last-24-hour bucket isn't 0.</p>
	</div>
</div>

<Load path="/features">
	{#snippet children(d)}
		<LineChart title="Installs using each feature, per day" series={d.series} by="feature" error={d.series_error} />
		<div class="grid section">
			<Bars title="Used in the last 24 h" items={d.features} total={d.installs} empty="No install reported use yet." />
			<Bars title="Install mode" items={d.modes} total={d.installs} fmt={mode} />
			<Bars title="Registration" items={d.registration} total={d.installs} />
			<Bars title="Sign-in providers (kinds)" items={d.sso} total={d.installs} fmt={sso} />
			<Bars title="Federation peers" items={d.federation} total={d.installs} fmt={(b) => b + ' peers'} />
			<Bars title="Profiles turned on" items={d.profiles} total={d.installs} fmt={(p) => p} />
		</div>
		<section class="section" aria-labelledby="h-set">
			<div class="section-head"><h2 id="h-set">Settings</h2><p>Unknown: the install's version doesn't report it.</p></div>
			<div class="table-wrap">
				<table>
					<thead><tr><th>Setting</th><th class="num">On</th><th class="num">Off</th><th class="num">Unknown</th></tr></thead>
					<tbody>
						{#each d.flags as f (f.name)}
							<tr>
								<td>{label(f.name)}</td>
								<td class="num">{num(f.true)} <span class="muted">{pct(f.true, d.installs)}</span></td>
								<td class="num">{num(f.false)}</td>
								<td class="num">{num(f.unknown)}</td>
							</tr>
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
</style>
