<script>
	// Daily points as lines: up to 4 series in the fixed series order plus a
	// grey "Other", a crosshair tooltip, a legend for 2+ series and a table.
	import { day, num, label as nice } from './fmt.js';

	/** @type {{ series: {labels: Record<string,string>, points: {t:number,v:number}[]}[], by?: string, title: string, empty?: string, error?: string }} */
	let { series = [], by = '', title, empty = 'No daily points yet: the first appear the day after the first reports.', error = '' } = $props();

	const H = 180;
	const pad = { l: 40, r: 12, t: 10, b: 24 };
	let width = $state(600);
	let hover = $state(-1);

	const name = (s) => (by ? s.labels?.[by] || '–' : title);

	// Top 4 by their latest value; the rest summed into Other.
	let lines = $derived.by(() => {
		const withLast = series
			.filter((s) => s.points?.length)
			.map((s) => ({ name: name(s), points: s.points, last: s.points[s.points.length - 1].v }));
		withLast.sort((a, b) => b.last - a.last || a.name.localeCompare(b.name));
		const top = withLast.slice(0, 4);
		// colour follows the entity: order the shown ones by name
		const order = [...top].sort((a, b) => a.name.localeCompare(b.name, 'en', { numeric: true }));
		const out = top.map((s) => ({ ...s, color: `var(--series-${order.indexOf(s) + 1})` }));
		const rest = withLast.slice(4);
		if (rest.length) {
			const sum = new Map();
			for (const s of rest) for (const p of s.points) sum.set(p.t, (sum.get(p.t) || 0) + p.v);
			out.push({
				name: `Other (${rest.length})`,
				points: [...sum].sort((a, b) => a[0] - b[0]).map(([t, v]) => ({ t, v })),
				color: 'var(--fg-3)'
			});
		}
		return out;
	});
	let ts = $derived([...new Set(lines.flatMap((l) => l.points.map((p) => p.t)))].sort((a, b) => a - b));
	let max = $derived(Math.max(1, ...lines.flatMap((l) => l.points.map((p) => p.v))));
	let niceMax = $derived.by(() => {
		const m = max;
		const step = Math.pow(10, Math.floor(Math.log10(m)));
		for (const k of [1, 2, 2.5, 5, 10]) if (k * step >= m) return k * step;
		return m;
	});
	let x = $derived((t) => {
		if (ts.length < 2) return pad.l + (width - pad.l - pad.r) / 2;
		return pad.l + ((t - ts[0]) / (ts[ts.length - 1] - ts[0])) * (width - pad.l - pad.r);
	});
	const y = (v) => pad.t + (1 - v / niceMax) * (H - pad.t - pad.b);
	const path = (pts) => pts.map((p, i) => `${i ? 'L' : 'M'}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join('');
	let ticks = $derived([0, niceMax / 2, niceMax]);
	let xticks = $derived(ts.length ? [ts[0], ts[Math.floor((ts.length - 1) / 2)], ts[ts.length - 1]] : []);

	function move(e) {
		if (!ts.length) return;
		const r = e.currentTarget.getBoundingClientRect();
		const px = e.clientX - r.left;
		let best = 0;
		for (let i = 0; i < ts.length; i++) if (Math.abs(x(ts[i]) - px) < Math.abs(x(ts[best]) - px)) best = i;
		hover = best;
	}
	const at = (l, t) => l.points.find((p) => p.t === t)?.v;
</script>

<figure class="chart">
	<figcaption>{title}</figcaption>
	{#if error}
		<p class="muted note">{error}: charts are unavailable, the tables below still work.</p>
	{:else if !lines.length}
		<p class="muted note">{empty}</p>
	{:else}
		{#if lines.length > 1}
			<ul class="legend">
				{#each lines as l (l.name)}
					<li><span class="key" style:background={l.color}></span>{nice(l.name)}</li>
				{/each}
			</ul>
		{/if}
		<div class="plot" bind:clientWidth={width}>
			<svg
				{width}
				height={H}
				role="img"
				aria-label="{title}, {ts.length} days"
				onpointermove={move}
				onpointerleave={() => (hover = -1)}
			>
				{#each ticks as t (t)}
					<line class="grid" x1={pad.l} x2={width - pad.r} y1={y(t)} y2={y(t)} />
					<text class="tick" x={pad.l - 6} y={y(t) + 4} text-anchor="end">{num(Math.round(t))}</text>
				{/each}
				{#each xticks as t, i (i)}
					<text class="tick" x={x(t)} y={H - 6} text-anchor={i === 0 ? 'start' : i === 2 ? 'end' : 'middle'}>{day(t)}</text>
				{/each}
				{#each lines as l (l.name)}
					<path d={path(l.points)} stroke={l.color} />
					{#if l.points.length === 1}
						<circle cx={x(l.points[0].t)} cy={y(l.points[0].v)} r="4" fill={l.color} />
					{/if}
				{/each}
				{#if hover >= 0}
					<line class="cross" x1={x(ts[hover])} x2={x(ts[hover])} y1={pad.t} y2={H - pad.b} />
					{#each lines as l (l.name)}
						{#if at(l, ts[hover]) != null}
							<circle class="dot-ring" cx={x(ts[hover])} cy={y(at(l, ts[hover]))} r="4" fill={l.color} />
						{/if}
					{/each}
				{/if}
			</svg>
			{#if hover >= 0}
				<div class="tip" style:left="{Math.min(Math.max(x(ts[hover]), 80), width - 80)}px">
					<strong>{day(ts[hover])}</strong>
					{#each lines as l (l.name)}
						<span><i style:background={l.color}></i>{nice(l.name)} <b>{num(at(l, ts[hover]) ?? 0)}</b></span>
					{/each}
				</div>
			{/if}
		</div>
		<details>
			<summary>Table</summary>
			<div class="table-wrap">
				<table>
					<thead><tr><th>Day</th>{#each lines as l (l.name)}<th class="num">{nice(l.name)}</th>{/each}</tr></thead>
					<tbody>
						{#each [...ts].reverse() as t (t)}
							<tr><td>{day(t)}</td>{#each lines as l (l.name)}<td class="num">{num(at(l, t) ?? 0)}</td>{/each}</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</details>
	{/if}
</figure>

<style>
	.chart {
		margin: 0;
		padding: var(--space-4);
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius-sm);
		min-width: 0;
	}
	figcaption {
		font-family: var(--font-display);
		font-weight: 600;
		font-size: var(--text-sm);
		margin-bottom: var(--space-3);
	}
	.note {
		font-size: var(--text-sm);
		padding: var(--space-6) 0;
	}
	.legend {
		list-style: none;
		margin: 0 0 var(--space-2);
		padding: 0;
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-1) var(--space-4);
		font-size: var(--text-sm);
		color: var(--fg-2);
	}
	.legend li {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-family: var(--font-mono);
	}
	.key {
		width: 10px;
		height: 2px;
		border-radius: 1px;
	}
	.plot {
		position: relative;
		width: 100%;
	}
	svg {
		display: block;
		touch-action: pan-y;
	}
	path {
		fill: none;
		stroke-width: 2;
		stroke-linejoin: round;
		stroke-linecap: round;
	}
	.grid {
		stroke: var(--chart-grid);
	}
	.cross {
		stroke: var(--chart-axis);
	}
	.dot-ring {
		stroke: var(--surface);
		stroke-width: 2;
	}
	.tick {
		fill: var(--fg-quiet);
		font-family: var(--font-mono);
		font-size: 10px;
	}
	.tip {
		position: absolute;
		top: 0;
		transform: translateX(-50%);
		pointer-events: none;
		display: grid;
		gap: 2px;
		padding: var(--space-2) var(--space-3);
		background: var(--surface-raised);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-sm);
		box-shadow: var(--shadow);
		font-size: var(--text-xs);
		font-family: var(--font-mono);
		white-space: nowrap;
	}
	.tip i {
		display: inline-block;
		width: 8px;
		height: 8px;
		border-radius: 2px;
		margin-right: 6px;
	}
	.tip b {
		margin-left: var(--space-2);
		color: var(--fg);
	}
	details {
		margin-top: var(--space-2);
		font-size: var(--text-sm);
	}
	summary {
		cursor: pointer;
		color: var(--fg-quiet);
		font-family: var(--font-mono);
		font-size: var(--text-xs);
	}
	details .table-wrap {
		margin-top: var(--space-2);
		max-height: 260px;
		overflow-y: auto;
	}
</style>
