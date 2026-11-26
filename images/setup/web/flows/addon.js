// "Add Boson to my existing IRC server": your server → what Boson gets →
// the bridge → accounts → (fresh.js's) domain, TLS, admin, features,
// ports → the config for your server (Re-check) → review → install → done.
// The wizard never edits the IRC server: it shows what to add, then checks.
import { h, put, api, ApiError, field, toggle, choices, notice, tag, button, errorMap } from '../lib.js';
import { useFlow, mountFlow, state, go, frame, card, nav, save, stepHref, nextOf, prevOf } from './fresh.js';

const BASE = '/api/flows/addon/';
const STEPS = [
	{ id: 'server', title: 'Your IRC server' },
	{ id: 'capabilities', title: 'What Boson gets' },
	{ id: 'bridge', title: 'The bridge' },
	{ id: 'accounts', title: 'Accounts' },
	{ id: 'domain', title: 'Domain' },
	{ id: 'tls', title: 'TLS certificates' },
	{ id: 'admin', title: 'Admin account' },
	{ id: 'features', title: 'Features' },
	{ id: 'ports', title: 'Firewall and ports' },
	{ id: 'snippets', title: 'Your server’s config' },
	{ id: 'review', title: 'Review' },
	{ id: 'install', title: 'Install' },
	{ id: 'done', title: 'Done' },
];

const SOFTWARE = { unrealircd: 'UnrealIRCd', inspircd: 'InspIRCd', ngircd: 'ngIRCd', ergo: 'Ergo', other: 'your IRC server', unknown: 'your IRC server' };
const STATUS = {
	native: { tone: 'ok', label: 'your server', head: 'From your server' },
	'via-bridge': { tone: 'accent', label: 'via the bridge', head: 'Through the bridge' },
	unavailable: { tone: 'bad', label: 'not available', head: 'Not available' },
};

let last = null; // the last probe result

export async function mount(c) {
	useFlow({ id: 'addon', steps: STEPS, renderers: { server, capabilities, bridge, accounts, snippets } });
	if (!last) {
		try {
			const r = await api('GET', BASE + 'probe');
			if (r.ok) last = r.result;
		} catch {
			/* none yet */
		}
	}
	return mountFlow(c);
}

const A = () => state.A;
const sw = () => SOFTWARE[A().addon.software] || 'your IRC server';

function needProbe(step) {
	frame(step, card('Check your IRC server first', null, notice('info', 'Setup needs to look at your IRC server before this step.'), h('div', { class: 'actions' }, h('a', { class: 'btn btn-primary', href: stepHref('server') }, 'Go to your IRC server'))));
}

// ---- server ----------------------------------------------------------------

function server(errors = {}) {
	const ad = A().addon;
	const typed = { token: '', account: '', password: '' };
	const result = h('div', { 'aria-live': 'polite' });
	let busy = false;
	const run = async () => {
		if (busy) return;
		busy = true;
		const btn = document.querySelector('[data-testid=next]');
		if (btn) btn.disabled = true;
		put(result, h('p', { class: 'muted' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), ' Connecting to your server, asking what it supports, testing WEBIRC and connection limits… (up to a minute)'));
		try {
			const r = await api('POST', BASE + 'probe', { host: ad.host, port: ad.port, tls: ad.tls, rpc_url: ad.rpc_url, rpc_token: typed.token, account: typed.account, password: typed.password });
			if (!r.ok) {
				put(result, notice('bad', h('strong', {}, 'Couldn’t check the server. '), r.error));
			} else {
				last = r.result;
				state.S = { ...state.S, answers: r.answers };
				if (last.rpc_error) {
					put(result, notice('bad', h('strong', {}, 'Services RPC: '), last.rpc_error), h('div', { class: 'actions' }, h('span'), h('div', { class: 'right' }, button('Continue without it', () => go('capabilities'), { testid: 'continue-rpc' }))));
				} else go('capabilities');
			}
		} catch (err) {
			put(result, err instanceof ApiError && err.status === 422 ? notice('bad', Object.values(errorMap(err)).join(' ')) : notice('bad', err.message));
		}
		busy = false;
		if (btn) btn.disabled = false;
	};
	frame(
		'server',
		card(
			'Your IRC server',
			'Boson keeps your IRC server as it is and adds itself next to it. Setup connects to it as a normal client first, to see what it supports. It never changes its config: later you get the lines to add yourself.',
			errors._ ? notice('bad', errors._) : null,
			h(
				'div',
				{ class: 'grid2' },
				field({ label: 'IRC server', name: 'addon.host', value: ad.host, errors, placeholder: 'irc.example.org', hint: 'Its hostname or address, as this server reaches it.', oninput: (e) => (ad.host = e.target.value.trim()) }),
				field({ label: 'Port', name: 'addon.port', value: String(ad.port || 6697), errors, inputmode: 'numeric', oninput: (e) => (ad.port = parseInt(e.target.value, 10) || 0) }),
			),
			toggle({ label: 'TLS', detail: 'Connect with TLS (usually port 6697). A self-signed certificate is pinned: the bridge accepts exactly that one.', checked: ad.tls, onchange: (v) => (ad.tls = v) }),
			h(
				'details',
				{ class: 'panel', open: !!ad.rpc_url },
				h('summary', {}, 'Services RPC (optional): reuse NickServ accounts'),
				h('p', { class: 'muted' }, 'With Anope 2.1’s or Atheme’s RPC, people can sign in to Boson with their existing NickServ account and password. Leave it empty if you don’t run it; you can turn it on later.'),
				h(
					'div',
					{ class: 'grid2' },
					field({ label: 'RPC address', name: 'addon.rpc_url', value: ad.rpc_url, errors, placeholder: 'http://services.example.org:8080/jsonrpc', oninput: (e) => (ad.rpc_url = e.target.value.trim()) }),
					field({ label: 'RPC token (Anope)', name: 'addon.rpc_token', type: 'password', errors, placeholder: state.S.secrets_set.rpc_token ? 'kept (type to replace)' : 'from Anope’s jsonrpc token block', oninput: (e) => (typed.token = e.target.value) }),
					field({ label: 'Test account (optional)', name: 'rpc_account', hint: 'An existing NickServ account, to check a sign-in works. Not stored.', oninput: (e) => (typed.account = e.target.value.trim()) }),
					field({ label: 'Its password', name: 'rpc_password', type: 'password', oninput: (e) => (typed.password = e.target.value) }),
				),
			),
			result,
			h('div', { class: 'actions' }, h('a', { class: 'btn btn-ghost', href: '#/' }, '← Start'), h('div', { class: 'right' }, button('Check the server', run, { kind: 'primary', testid: 'next' }))),
		),
	);
}

// ---- capabilities ----------------------------------------------------------

function capRow(c) {
	const st = STATUS[c.status] || STATUS.unavailable;
	return h(
		'li',
		{ 'data-feature': c.feature, 'data-status': c.status },
		tag(st.tone, st.label),
		h('span', {}, h('span', { class: 'name' }, c.title)),
		h('span', { class: 'msg' }, c.explanation, c.fix && c.status !== 'native' ? h('details', { class: 'fix' }, h('summary', {}, 'How to get it natively'), h('p', {}, c.fix)) : null),
	);
}

function capabilities() {
	if (!last) return needProbe('capabilities');
	const rep = last.report;
	const t = last.table;
	const groups = ['native', 'via-bridge', 'unavailable'].map((s) => [s, t.features.filter((f) => f.status === s)]);
	const ver = rep.server.version ? ' ' + rep.server.version : '';
	const svc = rep.services.package ? (rep.services.package === 'unknown' ? 'NickServ (unknown services)' : { anope: 'Anope', atheme: 'Atheme', ergo: 'Ergo’s NickServ' }[rep.services.package] + (rep.services.version ? ' ' + rep.services.version : '')) : 'no services';
	frame(
		'capabilities',
		card(
			'What Boson users get',
			`${SOFTWARE[rep.server.software] || rep.server.raw_version || 'An IRC server'}${ver} on ${rep.target.addr}${rep.server.network ? ' (' + rep.server.network + ')' : ''}, with ${svc}. For each Boson feature: whether your server provides it, the bridge adds it, or Boson goes without.`,
			last.tls_unverified ? notice('warn', h('strong', {}, 'Self-signed certificate. '), 'Its certificate isn’t from a trusted authority, so the bridge will accept exactly this one (SHA-256 ', h('code', {}, rep.target.cert_sha256), '). If it changes, run setup again.') : null,
			t.needs_bridge ? notice('info', h('strong', {}, 'Boson needs the bridge here. '), 'Some features your server lacks are added by the bridge, explained on the next page.') : notice('ok', 'Your server has everything Boson needs natively; the bridge still connects Boson accounts to it.'),
			...groups.map(([s, list]) =>
				list.length
					? h('div', { class: 'stack', 'data-group': s }, h('h2', {}, `${STATUS[s].head} (${list.length})`), h('ul', { class: 'checks caps', 'data-testid': `caps-${s}` }, list.map(capRow)))
					: null,
			),
			h('details', {}, h('summary', {}, 'The capability table as JSON'), h('pre', { class: 'code', 'data-testid': 'cap-table', tabindex: '0' }, JSON.stringify(t, null, 2))),
			nav('server', () => go('bridge')),
		),
	);
}

// ---- bridge ----------------------------------------------------------------

function bridge(errors = {}) {
	if (!last) return needProbe('bridge');
	const ad = A().addon;
	const d = A().domain || 'your-domain';
	const via = last.table.features.filter((f) => f.status === 'via-bridge').map((f) => f.title.toLowerCase());
	frame(
		'bridge',
		card(
			'The bridge',
			'Boson adds a bridge between Boson apps and your IRC server. This is what it is and what it does, in plain terms.',
			errors._ ? notice('bad', errors._) : null,
			h(
				'div',
				{ class: 'panel bridge-diagram', 'aria-label': 'How Boson users and classic IRC users reach your server' },
				h('div', { class: 'flowline' }, h('span', { class: 'node' }, 'Boson apps'), h('span', { class: 'arrow', 'aria-hidden': 'true' }, '→'), h('span', { class: 'node accent' }, `the bridge (irc.${d})`), h('span', { class: 'arrow', 'aria-hidden': 'true' }, '→'), h('span', { class: 'node' }, `${ad.host}:${ad.port}`)),
				h('div', { class: 'flowline' }, h('span', { class: 'node' }, 'Classic IRC clients'), h('span', { class: 'arrow', 'aria-hidden': 'true' }, '→'), h('span', { class: 'node' }, `${ad.host}:${ad.port}`), h('span', { class: 'muted' }, 'as today')),
			),
			h(
				'ul',
				{ class: 'plain' },
				h('li', {}, h('strong', {}, 'What it is: '), 'evrd, an IRC bouncer, running on this server in a mode made for Boson. It’s installed and updated with the rest of Boson.'),
				h('li', {}, h('strong', {}, 'Who uses it: '), 'only Boson apps (desktop, phone, web). Classic IRC clients keep connecting to your server directly and see Boson users as normal users.'),
				h('li', {}, h('strong', {}, 'What it adds: '), via.length ? via.join(', ') + '. ' : '', 'It keeps each Boson user connected to your server while their app is closed, so they keep their nick and get what they missed.'),
				h('li', {}, h('strong', {}, 'Addresses: '), `every Boson user connects to your server from this machine, with WEBIRC so your server sees their own address. Your server must trust this machine for that, and allow many connections from it (the snippets later do both).`),
				h('li', {}, h('strong', {}, 'What Boson users see: '), 'your network, under its own name. Nothing in Boson mentions the bridge.'),
			),
			h(
				'div',
				{ class: 'grid2' },
				field({
					label: 'This server’s address, as your IRC server sees it',
					name: 'addon.source_ip',
					value: ad.source_ip,
					errors,
					hint: `Detected from how ${sw()} greeted setup. Change it if this server reaches yours through NAT or another address.`,
					oninput: (e) => (ad.source_ip = e.target.value.trim()),
				}),
			),
			nav('capabilities', async () => {
				try {
					await save('irc');
					go('accounts');
				} catch (err) {
					bridge(err instanceof ApiError && err.status === 422 ? errorMap(err) : { _: err.message });
				}
			}),
		),
	);
}

// ---- accounts --------------------------------------------------------------

const STRATEGY = {
	anope_rpc: ['Anope’s RPC', 'Passwords are checked with anope.checkCredentials. Also gives Boson the account’s email.'],
	atheme_jsonrpc: ['Atheme’s JSON-RPC', 'Passwords are checked with atheme.login. New Boson sign-ups register NickServ accounts.'],
	atheme_xmlrpc: ['Atheme’s XML-RPC', 'As JSON-RPC, over XML-RPC.'],
	sasl: ['SASL on your IRC server', 'Boson checks passwords with SASL PLAIN on a connection that never joins. No email from the services.'],
	identify: ['IDENTIFY to NickServ (last resort)', 'Boson logs in a short-lived client and reads NickServ’s reply. Depends on its wording.'],
};

function accounts(errors = {}) {
	if (!last) return needProbe('accounts');
	const ad = A().addon;
	const rep = last.report;
	const rec = last.recommended;
	const hasNS = rep.services.nickserv;
	const rpc = rep.services.rpc && rep.services.rpc.reachable && rep.services.rpc.authorized ? rep.services.rpc : null;
	const _sasl = 'sasl' in (rep.caps || {});
	const avail = Object.keys(STRATEGY).filter((s) => {
		if (s === 'anope_rpc') return rpc && rpc.flavor === 'anope';
		if (s === 'atheme_jsonrpc') return rpc && rpc.flavor === 'atheme' && rpc.protocol === 'jsonrpc';
		if (s === 'atheme_xmlrpc') return rpc && rpc.flavor === 'atheme' && rpc.protocol === 'xmlrpc';
		if (s === 'sasl') return hasNS && _sasl;
		return hasNS;
	});
	const nsOK = last.nickserv_supported && hasNS;
	const recTag = (v) => (rec.accounts === v ? ' (recommended)' : '');
	const strat = h(
		'div',
		{ hidden: ad.accounts !== 'nickserv' },
		avail.length
			? choices({
					legend: 'How Boson checks NickServ passwords',
					name: 'strategy',
					value: ad.strategy || avail[0],
					options: avail.map((s) => ({ value: s, label: STRATEGY[s][0] + (rec.strategy === s ? ' (recommended)' : ''), detail: STRATEGY[s][1] })),
					onchange: (v) => (ad.strategy = v),
				})
			: null,
	);
	frame(
		'accounts',
		card(
			'Accounts',
			'How people sign in to Boson. Either way, each Boson user is on your network under their own nick.',
			errors._ ? notice('bad', errors._) : null,
			Object.keys(errors).filter((k) => k !== '_').length ? notice('bad', Object.values(errors).join(' ')) : null,
			notice('info', h('strong', {}, 'Recommended: ' + (rec.accounts === 'nickserv' ? 'NickServ accounts' : 'Boson accounts') + '. '), rec.why),
			choices({
				legend: 'Accounts',
				name: 'accounts',
				value: ad.accounts,
				options: [
					{ value: 'boson', label: 'Boson accounts' + recTag('boson'), detail: 'Boson has its own sign-up and passwords. A Boson user’s nick on your network is their Boson username; it isn’t linked to NickServ.' },
					{
						value: 'nickserv',
						label: 'Your NickServ accounts' + recTag('nickserv') + (nsOK ? '' : hasNS ? ' (needs a newer Boson)' : ' (no NickServ found)'),
						detail: 'People sign in to Boson with their NickServ account and password. Boson never stores those passwords; it checks them with your services each time.',
					},
				],
				onchange: (v) => {
					ad.accounts = v;
					if (v === 'nickserv' && !ad.strategy) ad.strategy = rec.strategy || avail[0] || '';
					strat.hidden = v !== 'nickserv';
				},
			}),
			!nsOK && hasNS ? notice('warn', 'This version of Boson’s account service can’t use NickServ accounts yet. Pick Boson accounts; you can switch when it can (run setup again).') : null,
			strat,
			nav('bridge', async () => {
				if (ad.accounts === 'nickserv' && !ad.strategy) ad.strategy = avail[0] || '';
				try {
					await save('irc');
					go(nextOf('accounts'));
				} catch (err) {
					accounts(err instanceof ApiError && err.status === 422 ? errorMap(err) : { _: err.message });
				}
			}),
		),
	);
	// Unsupported choice: disable it.
	if (!nsOK) {
		const r = document.querySelector('input[name=accounts][value=nickserv]');
		if (r) {
			r.disabled = true;
			r.closest('.choice').classList.add('disabled');
		}
	}
}

// ---- snippets --------------------------------------------------------------

function checkList(checks) {
	return h(
		'ul',
		{ class: 'checks', 'data-testid': 'needed' },
		checks.map((c) =>
			h(
				'li',
				{ 'data-check': c.id, 'data-ok': String(c.ok) },
				tag(c.ok ? 'ok' : 'bad', c.ok ? 'ok' : 'missing'),
				h('span', {}, h('span', { class: 'name' }, c.title)),
				h('span', { class: 'msg' }, c.detail, c.fix ? h('span', { class: 'fixline', 'data-testid': `fix-${c.id}` }, ' ', c.fix) : null),
			),
		),
	);
}

function snippetBox(s) {
	const pre = h('pre', { class: 'code', 'data-snippet': s.id, 'data-first': s.first ? 'true' : 'false', tabindex: '0' }, s.text);
	const status = h('span', { class: 'muted copied', 'aria-live': 'polite' });
	const copy = button(
		'Copy',
		async () => {
			try {
				await navigator.clipboard.writeText(s.text);
				status.textContent = 'Copied.';
			} catch {
				const r = document.createRange();
				r.selectNodeContents(pre);
				const sel = getSelection();
				sel.removeAllRanges();
				sel.addRange(r);
				status.textContent = 'Selected: press Ctrl+C (⌘C).';
			}
			setTimeout(() => (status.textContent = ''), 4000);
		},
		{ kind: 'small', testid: `copy-${s.id}` },
	);
	return h(
		'div',
		{ class: 'panel snippet', 'data-snippet-box': s.id },
		h('div', { class: 'snippet-head' }, h('h2', {}, s.title, s.optional ? h('span', { class: 'tag tag-accent' }, 'optional') : null, s.first ? h('span', { class: 'tag tag-warn' }, 'goes first') : null), h('span', { class: 'snippet-copy' }, status, copy)),
		h('p', { class: 'muted' }, s.where),
		s.warning ? notice('warn', h('strong', {}, 'Read this first. '), s.warning) : null,
		pre,
	);
}

function snippets() {
	if (!last) return needProbe('snippets');
	const ad = A().addon;
	const out = h('div', { class: 'stack' }, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Writing your config…'));
	const checksBox = h('div', { 'aria-live': 'polite', 'data-testid': 'checks-box', 'data-state': 'done' }, checkList(last.checks || []));
	let recheckBtn;
	let anyway = false;
	const nextBtn = () => document.querySelector('[data-testid=next]');
	const refresh = () => {
		const b = nextBtn();
		if (b) b.disabled = !(last.all_ok || anyway);
	};
	const recheck = async () => {
		recheckBtn.disabled = true;
		checksBox.dataset.state = 'checking';
		put(checksBox, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Checking your server again…'), checkList(last.checks || []));
		try {
			const r = await api('POST', BASE + 'probe', { recheck: true });
			if (!r.ok) put(checksBox, notice('bad', r.error), checkList(last.checks || []));
			else {
				last = r.result;
				put(checksBox, last.all_ok ? notice('ok', 'Everything Boson needs is in place.') : notice('bad', 'Not yet: ', last.checks.filter((c) => !c.ok).map((c) => c.title).join(', '), '.'), checkList(last.checks));
			}
		} catch (err) {
			put(checksBox, notice('bad', err.message));
		}
		recheckBtn.disabled = false;
		checksBox.dataset.state = 'done';
		refresh();
	};
	recheckBtn = button('Re-check', recheck, { kind: 'primary', testid: 'recheck' });
	const react =
		ad.software === 'unrealircd'
			? toggle({
					label: 'Also allow reactions (+draft/react)',
					detail: 'UnrealIRCd drops +draft/react by default. This adds a small module (source below) so Boson reactions reach everyone with an IRCv3 client.',
					checked: ad.allow_react,
					onchange: async (v) => {
						ad.allow_react = v;
						await save('').catch(() => {});
						snippets();
					},
				})
			: null;
	frame(
		'snippets',
		card(
			'Your server’s config',
			`Add these to ${sw()} yourself (setup never edits it), reload it, then press Re-check. The address in them, ${ad.source_ip}, is this server as yours sees it.`,
			h('div', { class: 'panel' }, h('h2', {}, 'What your server must do'), checksBox, h('div', {}, recheckBtn)),
			react,
			out,
			h(
				'div',
				{ class: 'actions' },
				h('a', { class: 'btn btn-ghost', href: stepHref(prevOf('snippets')) }, '← Back'),
				h(
					'div',
					{ class: 'right' },
					button(
						'Continue anyway',
						() => {
							anyway = true;
							go(nextOf('snippets'));
						},
						{ kind: 'ghost', testid: 'continue-anyway' },
					),
					button('Continue', () => go(nextOf('snippets')), { kind: 'primary', disabled: !last.all_ok, testid: 'next' }),
				),
			),
		),
	);
	(async () => {
		try {
			const r = await api('GET', BASE + 'snippets');
			const ircd = r.snippets.filter((s) => s.for !== 'services');
			const svc = r.snippets.filter((s) => s.for === 'services');
			put(
				out,
				h('h2', {}, `For ${sw()}`),
				...ircd.map(snippetBox),
				svc.length ? h('h2', {}, 'For your services (optional)') : null,
				...svc.map(snippetBox),
			);
		} catch (err) {
			put(out, notice('bad', err.message));
		}
	})();
}
