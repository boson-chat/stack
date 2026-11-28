// Formatting helpers (unit-tested in fmt.test.js).

const nf = new Intl.NumberFormat('en');

export function num(n) {
	return n == null ? '–' : nf.format(n);
}

export function pct(part, whole) {
	if (!whole) return '0%';
	const p = (100 * part) / whole;
	return (p > 0 && p < 1 ? '<1' : Math.round(p)) + '%';
}

// A bucket sum: "~mid (lo–hi)", or "≥ lo" when a 5k+ bucket is in it.
export function range(r) {
	if (!r || !r.reported) return { main: '–', sub: 'no installs reported it' };
	if (r.lo === r.hi) return { main: nf.format(r.lo) + (r.at_least ? '+' : ''), sub: `from ${r.reported} installs` };
	const mid = Math.round((r.lo + r.hi) / 2);
	return {
		main: (r.at_least ? '≥ ' + nf.format(r.lo) : '~' + nf.format(mid)),
		sub: `${nf.format(r.lo)}–${nf.format(r.hi)}${r.at_least ? '+' : ''} from ${r.reported} installs`
	};
}

let names;
export function country(code) {
	if (!code || code === 'ZZ') return 'Unknown';
	try {
		names ??= new Intl.DisplayNames(['en'], { type: 'region' });
		return names.of(code) || code;
	} catch {
		return code;
	}
}

// ISO code → flag emoji (regional indicators); none for ZZ.
export function flag(code) {
	if (!/^[A-Z]{2}$/.test(code || '') || code === 'ZZ') return '';
	return String.fromCodePoint(...[...code].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
}

export function ago(iso, now = Date.now()) {
	const t = typeof iso === 'number' ? iso : Date.parse(iso);
	if (!t) return '–';
	const s = Math.max(0, Math.round((now - t) / 1000));
	if (s < 60) return 'just now';
	const m = Math.round(s / 60);
	if (m < 60) return `${m} min ago`;
	const h = Math.round(m / 60);
	if (h < 48) return `${h} h ago`;
	return `${Math.round(h / 24)} days ago`;
}

export function day(t) {
	const d = new Date(typeof t === 'number' ? t * 1000 : t);
	return d.toLocaleDateString('en', { month: 'short', day: 'numeric', timeZone: 'UTC' });
}

export const labels = {
	update_failed: 'Failed update',
	rollback: 'Rollback',
	crash_loop: 'Crash loop',
	unhealthy: 'Unhealthy',
	cpu: 'High CPU',
	memory: 'High memory',
	disk: 'Disk full',
	guest_access: 'Guest access',
	guest_first: 'Guest-first web client',
	smtp: 'Email (SMTP) set up',
	directory_listed: 'Listed in a directory',
	voice: 'Voice',
	video: 'Video',
	files: 'File uploads',
	push: 'Push notifications',
	macos: 'macOS',
	windows: 'Windows',
	linux: 'Linux',
	ios: 'iOS',
	android: 'Android',
	web: 'Web'
};

export const label = (k) => labels[k] ?? k;

// A host_size bucket as words: ("cpu_cores", "3-4") → "3–4 cores".
export function size(dim, b) {
	if (b == null || b === 'unknown') return 'Unknown';
	const dash = (s) => s.replace('-', '–');
	switch (dim) {
		case 'cpu_cores':
			return dash(b) + (b === '1' ? ' core' : ' cores');
		case 'ram_gb':
		case 'disk_gb':
			if (b.startsWith('<')) return `Under ${b.slice(1)} GB`;
			if (b === '1000+') return '1 TB+';
			if (b === '500-1000') return '500 GB–1 TB';
			if (b.endsWith('+')) return `${b.slice(0, -1)} GB+`;
			return dash(b) + ' GB';
		case 'arch':
			return { amd64: 'amd64 (x86-64)', arm64: 'arm64', other: 'Other' }[b] ?? b;
	}
	return b;
}
