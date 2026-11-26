// "New Boson server": domain → TLS → admin → features → ports → review →
// install → done. Answers are saved on the server at every Continue (a
// reload or a restarted wizard comes back to them); secrets typed here are
// sent once and never shown again.
import { h, put, api, ApiError, field, select, toggle, choices, notice, tag, button, errorMap } from '../lib.js';

const FRESH_STEPS = [
	{ id: 'domain', title: 'Domain' },
	{ id: 'tls', title: 'TLS certificates' },
	{ id: 'admin', title: 'Admin account' },
	{ id: 'features', title: 'Features' },
	{ id: 'ports', title: 'Firewall and ports' },
	{ id: 'review', title: 'Review' },
	{ id: 'install', title: 'Install' },
	{ id: 'done', title: 'Done' },
];

// The flow these pages serve: fresh, or another that reuses them (the
// add-on path: useFlow({ id, steps, renderers, ... })).
const F = { id: 'fresh', steps: FRESH_STEPS, renderers: {}, after: {} };
let BASE = '/api/flows/fresh/';
let STEPS = FRESH_STEPS;

/** Serve another flow with these steps: its id, step list and extra pages. */
export function useFlow(cfg) {
	Object.assign(F, { renderers: {}, after: {} }, cfg);
	BASE = `/api/flows/${F.id}/`;
	STEPS = F.steps;
}

const stepHref = (id) => `#/${F.id}/${id}`;
const nextOf = (id) => STEPS[STEPS.findIndex((s) => s.id === id) + 1]?.id;
const prevOf = (id) => STEPS[STEPS.findIndex((s) => s.id === id) - 1]?.id;

let ctx;
let S = null; // server state
let A = null; // answers being edited
const typed = {}; // secrets typed on this page, until saved
let timers = [];
let stream = null;

function clearTimers() {
	timers.forEach(clearTimeout);
	timers = [];
}

const go = (id) => (location.hash = stepHref(id));

async function load() {
	S = await api('GET', BASE + 'state');
	A = structuredClone(S.answers);
}

async function save(section) {
	const secrets = {};
	for (const [k, v] of Object.entries(typed)) if (v) secrets[k] = v;
	S = await api('PUT', BASE + 'answers', { answers: A, secrets, section });
	A = structuredClone(S.answers);
	for (const k of Object.keys(typed)) delete typed[k];
}

function installLocked() {
	return S.running || S.install.status === 'done';
}

export async function mount(c) {
	if (F.id !== 'fresh') useFlow({ id: 'fresh', steps: FRESH_STEPS });
	return mountFlow(c);
}

/** Shared state for flows that reuse these pages. */
export const state = {
	get S() {
		return S;
	},
	get A() {
		return A;
	},
	set S(v) {
		S = v;
		A = structuredClone(v.answers);
	},
	typed,
};
export { go, frame, card, nav, save, saveAndGo, clearTimers, stepHref, nextOf, prevOf };

export async function mountFlow(c) {
	ctx = c;
	clearTimers();
	if (stream) {
		stream.close();
		stream = null;
	}
	await load();
	let step = c.step || STEPS[0].id;
	if (S.install.status === 'done' && step !== 'done') step = 'done';
	else if (S.running && step !== 'install') step = 'install';
	else if (S.install.status === 'failed' && !c.step) step = 'install';
	if (!STEPS.find((s) => s.id === step)) step = STEPS[0].id;
	if (c.step !== step) {
		history.replaceState(null, '', stepHref(step));
	}
	render(step);
}

function frame(step, body) {
	const idx = STEPS.findIndex((s) => s.id === step);
	const locked = installLocked();
	const installIdx = STEPS.findIndex((s) => s.id === 'install');
	const rail = h(
		'nav',
		{ class: 'rail', 'aria-label': 'Setup steps' },
		h(
			'ol',
			{},
			STEPS.map((s, i) => {
				const reachable = !locked ? i <= installIdx && s.id !== 'done' : s.id === step;
				const cur = s.id === step ? 'step' : undefined;
				const cls = i < idx ? 'done' : '';
				const inner = [h('span', { class: 'num' }, i < idx ? '✓' : String(i + 1)), h('span', {}, s.title)];
				return h('li', { 'aria-current': cur, class: cls }, reachable ? h('a', { href: stepHref(s.id) }, inner) : h('span', { class: 'item' }, inner));
			}),
		),
	);
	if (frame.last !== step) window.scrollTo(0, 0);
	frame.last = step;
	ctx.shell([rail, h('div', {}, h('p', { class: 'mobile-steps', 'aria-hidden': 'true' }, `Step ${idx + 1} of ${STEPS.length} · ${STEPS[idx].title}`), body)], { wide: true });
	document.title = `${STEPS[idx].title} · Boson setup`;
	ctx.focusHeading();
}

function card(title, lede, ...children) {
	return h('section', { class: 'card' }, h('header', {}, h('h1', {}, title), lede ? h('p', {}, lede) : null), ...children);
}

function nav(prev, next, { nextLabel = 'Continue', disabled = false, busy = false } = {}) {
	return h(
		'div',
		{ class: 'actions' },
		prev ? h('a', { class: 'btn btn-ghost', href: stepHref(prev) }, '← Back') : h('a', { class: 'btn btn-ghost', href: '#/' }, '← Start'),
		h('div', { class: 'right' }, next ? button(nextLabel, next, { kind: 'primary', disabled, busy, testid: 'next' }) : null),
	);
}

function render(step) {
	clearTimers();
	({ domain, tls, admin, features, ports, review, install, done, ...F.renderers })[step]();
}

/** Saves a section and moves on; shows field errors on the page otherwise. */
async function saveAndGo(section, next, rerender) {
	try {
		await save(section);
		go(next);
	} catch (err) {
		if (err instanceof ApiError && err.status === 422) rerender(errorMap(err));
		else rerender({ _: err.message });
	}
}

// ---- domain ----------------------------------------------------------------

let lastDNS = null;

function dnsList(sum) {
	if (!sum) return h('p', { class: 'muted' }, h('span', { class: 'spinner', 'aria-hidden': 'true' }), ' Checking DNS…');
	return h(
		'ul',
		{ class: 'checks', 'data-testid': 'dns-results' },
		sum.hosts.map((r) =>
			h(
				'li',
				{ 'data-host': r.name, 'data-status': r.status },
				tag({ ok: 'ok', missing: 'warn', elsewhere: 'bad', error: 'warn' }[r.status], { ok: 'ok', missing: 'missing', elsewhere: 'wrong', error: 'retrying' }[r.status]),
				h('span', {}, h('span', { class: 'name' }, r.name), ' ', h('span', { class: 'what' }, r.what)),
				h('span', { class: 'msg' }, r.message, r.a.length + r.aaaa.length ? h('span', { class: 'what' }, ` (${[...r.a, ...r.aaaa].join(', ')})`) : null),
			),
		),
	);
}

function dnsAdvice(sum) {
	if (!sum) return null;
	const ips = sum.server_ips.filter((ip) => !ip.includes(':')).slice(0, 1);
	const bad = sum.hosts.filter((x) => x.status === 'elsewhere');
	return h(
		'div',
		{ class: 'stack' },
		bad.length
			? notice(
					'bad',
					h('strong', {}, 'DNS points somewhere else.'),
					` ${bad.length === 1 ? bad[0].name + ' points' : bad.length + ' names point'} to ${[...new Set(bad.flatMap((b) => [...b.a, ...b.aaaa]))].join(', ')}, which is not this server${ips.length ? ' (' + ips[0] + ')' : ''}. Change the records at your DNS provider; this page checks again every few seconds.`,
				)
			: null,
		sum.ok
			? notice('ok', 'Every name points at this server.')
			: notice(
					'info',
					`Add an A record for each name above (or one wildcard record, *.${A.domain}) with this server’s address${ips.length ? ': ' + ips[0] : ''}. If your server has IPv6, add AAAA records too, or none at all.`,
				),
	);
}

function domain(errors = {}) {
	let busy = false;
	const list = h('div', { 'aria-live': 'polite' }, A.domain ? dnsList(lastDNS) : h('p', { class: 'muted' }, 'Enter your domain to check its DNS.'));
	const advice = h('div', {}, A.domain && lastDNS ? dnsAdvice(lastDNS) : null);
	const nextBtn = () => document.querySelector('[data-testid=next]');
	let dnsOK = !!(lastDNS && lastDNS.ok && lastDNS.domain === A.domain);

	async function check() {
		if (!A.domain) return;
		try {
			const sum = await api('POST', BASE + 'check/dns', {});
			sum.domain = A.domain;
			lastDNS = sum;
			dnsOK = sum.ok;
			put(list, dnsList(sum));
			put(advice, dnsAdvice(sum));
			const b = nextBtn();
			if (b) b.disabled = !dnsOK;
		} catch (err) {
			put(list, notice('warn', err.message));
		}
		if (!dnsOK) timers.push(setTimeout(check, 5000));
	}

	let debounce;
	const onDomain = (e) => {
		A.domain = e.target.value.trim().toLowerCase();
		clearTimeout(debounce);
		clearTimers();
		dnsOK = false;
		const b = nextBtn();
		if (b) b.disabled = true;
		debounce = setTimeout(async () => {
			try {
				await save('domain');
				lastDNS = null;
				put(list, dnsList(null));
				put(advice, );
				check();
			} catch (err) {
				put(list, notice('warn', (err.body && err.body.errors && err.body.errors[0].message) || err.message));
			}
		}, 600);
	};

	frame(
		'domain',
		card(
			'Your domain',
			'Boson uses a few names under one domain: irc., auth., chat. (the web client), admin., api. and one per feature. Each must point at this server.',
			S.existing.found ? notice('info', `Found an existing install in ${S.existing.dir}. Its settings are filled in and its secrets will be kept.`) : null,
			errors._ ? notice('bad', errors._) : null,
			h(
				'div',
				{ class: 'grid2' },
				field({ label: 'Network name', name: 'network_name', value: A.network_name, errors, hint: 'One word (no spaces): the IRC network name, also shown on sign-in pages and in emails.', oninput: (e) => (A.network_name = e.target.value) }),
				field({ label: 'Domain', name: 'domain', value: A.domain, errors, placeholder: 'chat.example.com', inputmode: 'url', hint: 'No https://. A subdomain works too.', oninput: onDomain }),
			),
			toggle({
				label: `Also serve app discovery at https://${A.domain || 'your-domain'}`,
				detail: 'Lets the Boson app find this server from the bare domain (/.well-known/boson.json). Only if the domain itself points here, not at your website.',
				checked: A.wellknown,
				onchange: async (v) => {
					A.wellknown = v;
					await save('').catch(() => {});
					lastDNS = null;
					put(list, dnsList(null));
					clearTimers();
					check();
				},
			}),
			h('div', { class: 'stack' }, h('h2', {}, 'DNS check'), list, advice),
			nav(prevOf('domain'), () => saveAndGo('domain', nextOf('domain'), domain), { disabled: !dnsOK, busy }),
		),
	);
	if (A.domain) check();
}

// ---- tls -------------------------------------------------------------------

function tls(errors = {}) {
	const custom = h('div', { hidden: A.tls.ca !== 'custom' }, field({ label: 'ACME directory URL', name: 'tls.custom_url', value: A.tls.custom_url, errors, placeholder: 'https://acme.example.com/directory', oninput: (e) => (A.tls.custom_url = e.target.value) }));
	frame(
		'tls',
		card(
			'TLS certificates',
			'Traefik, the stack’s front door, gets a certificate for every name from Let’s Encrypt and renews it by itself. IRC and voice get copies of the same certificates.',
			errors._ ? notice('bad', errors._) : null,
			field({ label: 'Email for Let’s Encrypt', name: 'tls.email', type: 'email', value: A.tls.email || A.admin.email, errors, hint: 'Let’s Encrypt writes here if a certificate is about to expire unrenewed.', oninput: (e) => (A.tls.email = e.target.value) }),
			choices({
				legend: 'Certificate authority',
				name: 'ca',
				value: A.tls.ca,
				options: [
					{ value: 'production', label: 'Let’s Encrypt', detail: 'Trusted everywhere. The right choice for a real server.' },
					{ value: 'staging', label: 'Let’s Encrypt staging', detail: 'Untrusted test certificates with no rate limits, for trying things out.' },
					{ value: 'custom', label: 'Another ACME server', detail: 'Your own ACME CA (advanced).' },
				],
				onchange: (v) => {
					A.tls.ca = v;
					custom.hidden = v !== 'custom';
				},
			}),
			custom,
			notice('info', 'Let’s Encrypt checks each name over port 80, so 80/tcp must be open to the internet. The ports step tests what it can.'),
			nav(prevOf('tls'), () => saveAndGo('tls', nextOf('tls'), tls)),
		),
	);
}

// ---- admin -----------------------------------------------------------------

function admin(errors = {}) {
	const exists = S.existing.admin_created;
	const ns = A.mode === 'addon' && A.addon && A.addon.accounts === 'nickserv';
	let confirmErr = '';
	const pw = field({ label: exists ? 'Password' : 'Password', name: 'admin.password', type: 'password', autocomplete: 'new-password', errors, hint: exists ? 'Unchanged: the account exists already.' : 'At least 10 characters.', oninput: (e) => (typed.admin_password = e.target.value) });
	const pw2 = field({ label: 'Password again', name: 'admin.password2', type: 'password', autocomplete: 'new-password', errors });
	if (exists) {
		pw.querySelector('input').disabled = true;
		pw2.querySelector('input').disabled = true;
	}
	if (ns) {
		pw.hidden = true;
		pw2.hidden = true;
	}
	frame(
		'admin',
		card(
			'Your admin account',
			ns
				? 'Your NickServ account. Boson creates it in its account service with the Owner role (full control of Boson and the admin panel); you sign in with your NickServ password, which Boson never stores.'
				: A.mode === 'addon'
					? 'Created in Boson’s account service once it’s running, with the Owner role: full control of Boson and the admin panel.'
					: 'Created in Boson’s account service once it’s running, with the Owner role: full control of the network, the admin panel and IRC operator powers.',
			exists ? notice('info', `${A.admin.username} exists already and keeps its password. Change the username to add another owner.`) : null,
			errors._ ? notice('bad', errors._) : null,
			h(
				'div',
				{ class: 'grid2' },
				field({ label: ns ? 'NickServ account' : 'Username', name: 'admin.username', value: A.admin.username, errors, autocomplete: 'username', hint: ns ? 'The account name you identify to NickServ with.' : 'Also your IRC nick.', oninput: (e) => (A.admin.username = e.target.value) }),
				field({ label: 'Email', name: 'admin.email', type: 'email', value: A.admin.email, errors, autocomplete: 'email', oninput: (e) => (A.admin.email = e.target.value) }),
				pw,
				pw2,
			),
			nav(prevOf('admin'), () => {
				if (!exists && !ns) {
					const a = pw.querySelector('input').value;
					const b = pw2.querySelector('input').value;
					if (a !== b) {
						confirmErr = 'The passwords don’t match.';
						return admin({ ...errors, 'admin.password2': confirmErr });
					}
					if (!a && S.secrets_set.admin_password) delete typed.admin_password;
				}
				if (!A.tls.email) A.tls.email = A.admin.email;
				saveAndGo('admin', nextOf('admin'), admin);
			}),
		),
	);
}

// ---- features --------------------------------------------------------------

function features(errors = {}) {
	const section = (title, ...kids) => h('div', { class: 'panel' }, h('h2', {}, title), ...kids);
	const s3 = h(
		'div',
		{ class: 'grid2', hidden: A.files.storage !== 's3' },
		field({ label: 'S3 endpoint', name: 'files.s3_endpoint', value: A.files.s3_endpoint, errors, placeholder: 'https://s3.eu-central-1.amazonaws.com', oninput: (e) => (A.files.s3_endpoint = e.target.value) }),
		field({ label: 'Bucket', name: 'files.s3_bucket', value: A.files.s3_bucket, errors, oninput: (e) => (A.files.s3_bucket = e.target.value) }),
		field({ label: 'Region', name: 'files.s3_region', value: A.files.s3_region, errors, oninput: (e) => (A.files.s3_region = e.target.value) }),
		h('div'),
		field({ label: 'Access key', name: 'files.s3_keys', errors, placeholder: S.secrets_set.s3_keys ? 'kept (type to replace)' : '', oninput: (e) => (typed.s3_access_key = e.target.value) }),
		field({ label: 'Secret key', name: 'files.s3_secret', type: 'password', placeholder: S.secrets_set.s3_keys ? 'kept (type to replace)' : '', oninput: (e) => (typed.s3_secret_key = e.target.value) }),
	);
	const filesOpts = h(
		'div',
		{ class: 'stack', hidden: !A.files.enabled },
		choices({
			legend: 'Where uploads are stored',
			name: 'storage',
			value: A.files.storage,
			options: [
				{ value: 'local', label: 'This server’s disk', detail: 'Simplest. Uses a Docker volume; mind the free space.' },
				{ value: 'minio', label: 'MinIO on this server', detail: 'An internal S3 store, in case you move to S3 later.' },
				{ value: 's3', label: 'An S3 bucket', detail: 'AWS S3, Cloudflare R2, Backblaze B2, or any S3-compatible store.' },
			],
			onchange: (v) => {
				A.files.storage = v;
				s3.hidden = v !== 's3';
			},
		}),
		s3,
	);
	const videoT = toggle({ label: 'Video and screen share', detail: 'Cameras and screens in voice channels (UDP 7710).', checked: A.video, disabled: !A.voice, onchange: (v) => (A.video = v) });
	const guestVoiceT = toggle({ label: 'Guests in voice', detail: 'Guests can join voice channels too, under their nick with a guest badge.', checked: A.guest_voice, disabled: !A.guests || !A.voice, onchange: (v) => (A.guest_voice = v) });
	const refreshDeps = () => {
		const vi = videoT.querySelector('input');
		vi.disabled = !A.voice;
		videoT.classList.toggle('disabled', !A.voice);
		if (!A.voice) vi.checked = A.video = false;
		const gi = guestVoiceT.querySelector('input');
		gi.disabled = !A.guests || !A.voice;
		guestVoiceT.classList.toggle('disabled', gi.disabled);
		if (gi.disabled) gi.checked = A.guest_voice = false;
	};

	const federation = federationSection(section, errors);

	const smtpResult = h('div', { 'aria-live': 'polite' });
	const smtpTLS = select({
		label: 'Security',
		name: 'smtp.tls',
		value: A.smtp.tls,
		options: [
			['starttls', 'STARTTLS (usually port 587)'],
			['tls', 'TLS (usually port 465)'],
			['none', 'None (local relay only)'],
		],
		onchange: (e) => (A.smtp.tls = e.target.value),
	});
	const testTo = field({ label: 'Send a test to', name: 'smtp.test_to', type: 'email', value: A.admin.email });
	const sendTest = button(
		'Send test email',
		async () => {
			sendTest.disabled = true;
			put(smtpResult, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Sending…'));
			try {
				await save('');
				const r = await api('POST', BASE + 'check/smtp', { to: testTo.querySelector('input').value });
				put(smtpResult, r.ok ? notice('ok', 'Sent. Check the inbox (and the spam folder).') : notice('bad', 'Not sent: ' + r.error));
			} catch (err) {
				put(smtpResult, notice('bad', err.message));
			}
			sendTest.disabled = false;
		},
		{ testid: 'smtp-test' },
	);
	const smtp = section(
		'Outgoing email (SMTP)',
		h('p', { class: 'muted' }, 'For sign-up verification, password resets and invites. Leave the server empty to skip it for now; you can add it later in the admin panel.'),
		h(
			'div',
			{ class: 'grid2' },
			field({ label: 'Mail server', name: 'smtp.host', value: A.smtp.host, errors, placeholder: 'smtp.example.com', oninput: (e) => (A.smtp.host = e.target.value) }),
			field({ label: 'Port', name: 'smtp.port', value: String(A.smtp.port || 587), errors, inputmode: 'numeric', oninput: (e) => (A.smtp.port = parseInt(e.target.value, 10) || 0) }),
			field({ label: 'Sender', name: 'smtp.from', value: A.smtp.from, errors, placeholder: `${A.network_name} <noreply@${A.domain}>`, oninput: (e) => (A.smtp.from = e.target.value) }),
			smtpTLS,
			field({ label: 'User name', name: 'smtp.user', value: A.smtp.user, errors, oninput: (e) => (A.smtp.user = e.target.value) }),
			field({ label: 'Password', name: 'smtp.password', type: 'password', autocomplete: 'new-password', placeholder: S.secrets_set.smtp_password ? 'kept (type to replace)' : '', oninput: (e) => (typed.smtp_password = e.target.value) }),
		),
		h('div', { class: 'grid2' }, testTo, h('div', { class: 'field' }, h('span', { 'aria-hidden': 'true' }, ' '), sendTest)),
		smtpResult,
	);

	frame(
		'features',
		card(
			'Features',
			'Pick what this server offers. Everything here can be changed later in the admin panel or by running setup again.',
			errors._ ? notice('bad', errors._) : null,
			Object.keys(errors).filter((k) => k !== '_').length ? notice('bad', 'Some fields need a look: ' + Object.values(errors).join(' ')) : null,
			section(
				'Calls',
				toggle({
					label: 'Voice',
					detail: 'Voice channels and calls (UDP 7700).',
					checked: A.voice,
					onchange: (v) => {
						A.voice = v;
						refreshDeps();
					},
				}),
				videoT,
			),
			section(
				'Uploads',
				toggle({
					label: 'File uploads',
					detail: 'Images and files shared in chat, with a per-person quota.',
					checked: A.files.enabled,
					onchange: (v) => {
						A.files.enabled = v;
						filesOpts.hidden = !v;
					},
				}),
				filesOpts,
			),
			section(
				'Who can join',
				choices({
					legend: 'Sign-up',
					name: 'registration',
					value: A.registration,
					options: [
						{ value: 'open', label: 'Open', detail: 'Anyone can create an account.' },
						{ value: 'invite', label: 'Invite only', detail: 'People need an invite from an admin.' },
						{ value: 'closed', label: 'Closed', detail: 'No new accounts; admins create them.' },
					],
					onchange: (v) => (A.registration = v),
				}),
				toggle({
					label: 'Guests',
					detail: A.mode === 'addon' ? 'Not on this path yet: the bridge signs every Boson user in. People without an account can still use your IRC server directly.' : 'People can chat on IRC without an account, under a nick.',
					disabled: A.mode === 'addon',
					checked: A.guests,
					onchange: (v) => {
						A.guests = v;
						refreshDeps();
					},
				}),
				guestVoiceT,
				toggle({ label: 'Require email verification', detail: 'New accounts confirm their email before signing in. Needs outgoing email.', checked: A.email_verification, onchange: (v) => (A.email_verification = v) }),
			),
			section(
				'Directory',
				toggle({ label: 'List this network in the Boson directory', detail: 'So people can find it in the app. You finish the listing in the admin panel (a DNS TXT record proves the domain is yours).', checked: A.directory_listing, onchange: (v) => (A.directory_listing = v) }),
				toggle({ label: 'Run a directory of my own', detail: `A network directory at directory.${A.domain || 'your-domain'}, federated with directory.boson.chat (advanced).`, checked: A.own_directory, onchange: (v) => (A.own_directory = v) }),
			),
			federation,
			smtp,
			nav(prevOf('features'), () => saveAndGo('features', nextOf('features'), features)),
		),
	);
}

// Federated sign-in (docs/federation.md): who may sign in here with an
// account from another Boson server. On the add-on path remote users reach
// the IRC server through the bridge (docs/bridge.md), without a services
// account there.
function federationSection(section, errors) {
	const title = 'Accounts from other servers';
	const addonNote =
		A.mode === 'addon'
			? h(
					'p',
					{ class: 'muted', 'data-testid': 'federation-addon-note' },
					'On your IRC server they join through the bridge, without a services account there. Their nick carries their home server (alice-bosonchat), and the bridge keeps them off nicks registered with your services.',
				)
			: null;
	const allow = field({
		label: 'Servers to accept',
		name: 'federation_allow',
		value: (A.federation_allow || '').split(',').filter(Boolean).join(', '),
		errors,
		placeholder: 'boson.chat, friends.example.org',
		hint: 'Their domains, separated by commas. *.example.org also takes its subdomains.',
		oninput: (e) => (A.federation_allow = e.target.value),
	});
	const allowBox = h('div', { class: 'stack', hidden: A.federation !== 'allowlist' }, allow);
	const discovery = notice('warn', `People here can’t use their account on other servers until app discovery is on (https://${A.domain || 'your-domain'}/.well-known/boson.json, on the Domain step): that’s where other servers check who you are.`);
	discovery.hidden = A.wellknown || A.federation === 'off';
	return section(
		title,
		h('p', { class: 'muted' }, 'People with an account on another Boson server can join here without signing up, and people from here can do the same elsewhere. No passwords or messages cross servers, only a short-lived signed note of who someone is. They follow this server’s rules, and you can ban them or block their whole server later.'),
		addonNote,
		errors.federation ? notice('bad', errors.federation) : null,
		choices({
			legend: 'Accept accounts from',
			name: 'federation',
			value: A.federation || 'directory',
			options: [
				{ value: 'directory', label: 'Verified servers (recommended)', detail: 'Servers with a verified listing in the Boson directory: their owner proved the domain is theirs.' },
				{ value: 'allowlist', label: 'Only servers I list', detail: 'The tightest choice. You add each server you trust.' },
				{ value: 'open', label: 'Any Boson server', detail: 'Anyone can set up a server, spammers too. Only with active moderation.' },
				{ value: 'off', label: 'Off', detail: 'Only accounts made here. People from here can’t use their account on other servers either.' },
			],
			onchange: (v) => {
				A.federation = v;
				allowBox.hidden = v !== 'allowlist';
				discovery.hidden = A.wellknown || v === 'off';
			},
		}),
		allowBox,
		discovery,
	);
}

// ---- ports -----------------------------------------------------------------

function ports() {
	const out = h('div', { 'aria-live': 'polite', class: 'stack' }, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Testing ports from inside this server…'));
	let report = null;
	let recheck;
	const run = async () => {
		if (recheck) recheck.disabled = true;
		put(out, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Testing ports from inside this server…'));
		try {
			report = await api('POST', BASE + 'check/ports', {});
		} catch (err) {
			put(out, notice('bad', err.message));
			return;
		} finally {
			if (recheck) recheck.disabled = false;
		}
		const off = async (feature) => {
			if (feature === 'voice') A.voice = A.video = false;
			if (feature === 'video') A.video = false;
			await save('');
			run();
		};
		put(out, 
			h(
				'ul',
				{ class: 'checks', 'data-testid': 'port-results' },
				report.ports.map((p) =>
					h(
						'li',
						{ 'data-port': `${p.port}/${p.proto}`, 'data-level': p.level },
						tag(p.level === 'ok' ? 'ok' : p.level === 'warn' ? 'warn' : 'bad', p.level === 'ok' ? 'ok' : p.level === 'warn' ? 'warning' : 'problem'),
						h('span', {}, h('span', { class: 'name' }, `${p.port}/${p.proto}`), ' ', h('span', { class: 'what' }, p.what)),
						h(
							'span',
							{ class: 'msg' },
							p.message,
							p.level !== 'ok' && (p.feature === 'voice' || p.feature === 'video') ? h('span', {}, ' ', button(`Turn ${p.feature} off`, () => off(p.feature), { kind: 'small', testid: `off-${p.feature}` })) : null,
						),
					),
				),
			),
			report.probe !== 'ran' ? notice('warn', 'The firewall test couldn’t run here (' + report.probe + '). Only whether the ports are free was checked.') : null,
			notice('info', h('strong', {}, 'What this can’t check. '), report.outside, ' Once installed, try the web client from your phone on mobile data to be sure.'),
		);
	};
	frame(
		'ports',
		card(
			'Firewall and ports',
			'Boson needs these ports open to the internet. This test runs from inside the server: it finds programs already using a port and this server’s own firewall blocking one.',
			out,
			h('div', {}, (recheck = button('Check again', run, { testid: 'ports-recheck' }))),
			nav(prevOf('ports'), () => go(nextOf('ports'))),
		),
	);
	run();
}

// ---- review ----------------------------------------------------------------

function review(error) {
	const body = h('div', { class: 'stack' }, h('p', { class: 'muted' }, h('span', { class: 'spinner' }), ' Building the configuration…'));
	const dns = h('div', { 'aria-live': 'polite' });
	let installBtn;
	const start = async () => {
		installBtn.disabled = true;
		try {
			await api('POST', BASE + 'install', {});
			go('install');
		} catch (err) {
			installBtn.disabled = false;
			if (err.code === 'dns') {
				put(dns, notice('bad', h('strong', {}, 'Not all names point at this server yet. '), 'Fix DNS, then try again.'), dnsList(err.body.dns));
			} else if (err.code === 'admin') {
				put(dns, notice('bad', err.message, ' ', h('a', { href: stepHref('admin') }, 'Go to the admin step')));
			} else put(dns, notice('bad', err.message));
		}
	};
	installBtn = button(S.existing.found ? 'Apply and restart' : 'Install Boson', start, { kind: 'primary', testid: 'install' });
	frame(
		'review',
		card(
			'Review',
			'This is what will be written. Secrets are shown as dots: they are random, or kept from your existing install, and stay on this server.',
			error ? notice('bad', error) : null,
			body,
			dns,
			h('div', { class: 'actions' }, h('a', { class: 'btn btn-ghost', href: stepHref(prevOf('review')) }, '← Back'), h('div', { class: 'right' }, installBtn)),
		),
	);
	(async () => {
		try {
			const r = await api('GET', BASE + 'review');
			put(body, 
				h(
					'div',
					{ class: 'panel' },
					h('h2', {}, 'Summary'),
					h(
						'ul',
						{ class: 'checks' },
						h('li', {}, tag('accent', 'dir'), h('span', { class: 'name' }, r.dir)),
						h('li', {}, tag('accent', 'parts'), h('span', { class: 'name' }, r.profiles.join(', '))),
						h('li', {}, tag('accent', 'names'), h('span', { class: 'name' }, r.hosts.map((x) => x.name).join(', '))),
						r.existing ? h('li', {}, tag('ok', 'kept'), h('span', { class: 'name' }, `${r.kept_secrets} secrets kept from your .env; other lines in it stay as they are`)) : null,
					),
				),
				h('details', { open: true }, h('summary', {}, '.env (secrets masked)'), h('pre', { class: 'code', 'data-testid': 'review-env', tabindex: '0' }, r.env)),
				(r.files || []).map((f) => h('details', { open: true }, h('summary', {}, f.path), h('pre', { class: 'code', 'data-testid': 'review-file', 'data-path': f.path, tabindex: '0' }, f.content))),
				h(
					'details',
					{ open: true },
					h('summary', {}, r.config_new ? 'config/boson.yaml (new; the reference comments follow below these keys)' : r.config_changed ? 'config/boson.yaml (your file, with these changes)' : 'config/boson.yaml (unchanged)'),
					h('pre', { class: 'code', 'data-testid': 'review-config', tabindex: '0' }, r.config),
				),
			);
		} catch (err) {
			put(body, notice('bad', err.message));
			installBtn.disabled = true;
		}
	})();
}

// ---- install ---------------------------------------------------------------

const ICON = { pending: '○', running: '◌', done: '✓', failed: '✕' };

function install() {
	const steps = h('ol', { class: 'checks', 'data-testid': 'install-steps' });
	const failure = h('div', { 'aria-live': 'assertive' });
	const services = h('div');
	const log = h('pre', { class: 'code log', 'data-testid': 'install-log', tabindex: '0', 'aria-label': 'Install log' });
	let lines = [];

	const draw = (st, running) => {
		put(steps, 
			...st.steps.map((s) =>
				h(
					'li',
					{ 'data-step': s.id, 'data-status': s.status },
					h('span', { class: 'num', 'aria-hidden': 'true' }, s.status === 'running' ? h('span', { class: 'spinner' }) : ICON[s.status]),
					h('span', {}, h('span', { class: 'name' }, s.title), h('span', { class: 'sr-only' }, `: ${s.status}`)),
					s.status === 'failed' ? h('span', { class: 'msg' }, s.hint ? s.hint.replace(/^[a-z_]+: /, '') : '') : null,
				),
			),
		);
		const f = st.steps.find((s) => s.status === 'failed');
		if (f && !running) {
			const needPw = (f.hint || '').startsWith('need_admin_password');
			put(failure, 
				notice(
					'bad',
					h('strong', {}, `“${f.title}” failed.`),
					h('pre', { class: 'code log', 'data-testid': 'install-error' }, f.error),
					h(
						'div',
						{ class: 'actions' },
						h('a', { class: 'btn btn-ghost', href: stepHref(needPw ? 'admin' : 'review') }, needPw ? 'Enter the admin password' : 'Change settings'),
						h('div', { class: 'right' }, button('Retry', retry, { kind: 'primary', testid: 'retry' })),
					),
				),
			);
		} else put(failure, );
		if (st.status === 'done') {
			if (stream) stream.close();
			setTimeout(() => go('done'), 600);
		}
	};
	const drawServices = (list) => {
		put(services, 
			h('details', { open: true }, h('summary', {}, `Services (${list.filter((s) => s.ready).length} of ${list.length} ready)`),
				h(
					'ul',
					{ class: 'checks', 'data-testid': 'services' },
					list.map((s) =>
						h('li', { 'data-service': s.service }, tag(s.ready ? 'ok' : s.failed ? 'bad' : 'warn', s.ready ? 'ready' : s.failed ? 'failing' : 'starting'), h('span', { class: 'name' }, s.service, h('span', { class: 'what' }, ` ${s.state}${s.health ? ', ' + s.health : ''}`))),
					),
				),
			),
		);
	};
	async function retry() {
		try {
			await api('POST', BASE + 'install', {});
			lines = [];
			log.textContent = '';
			connect();
		} catch (err) {
			put(failure, notice('bad', err.message, err.code === 'dns' ? ' (DNS changed: see the Domain step.)' : ''));
		}
	}
	function connect() {
		if (stream) stream.close();
		stream = new EventSource(BASE + 'install/events');
		stream.onmessage = (m) => {
			const e = JSON.parse(m.data);
			if (e.type === 'state') {
				S.install = e.state;
				S.running = e.state.status === 'running';
				draw(e.state, S.running);
			} else if (e.type === 'log') {
				lines.push(e.line);
				if (lines.length > 300) lines = lines.slice(-300);
				const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 8;
				log.textContent = lines.join('\n');
				if (atEnd) log.scrollTop = log.scrollHeight;
			} else if (e.type === 'services') drawServices(e.services);
		};
	}
	frame(
		'install',
		card(
			S.existing.found ? 'Applying your settings' : 'Installing Boson',
			`Writing the configuration to ${S.existing.dir}, then starting the stack with Docker Compose. This takes a few minutes; you can leave this page open or come back to it.`,
			steps,
			failure,
			services,
			h('details', { open: true }, h('summary', {}, 'Log'), log),
		),
	);
	draw(S.install, S.running);
	connect();
}

// ---- done ------------------------------------------------------------------

async function done() {
	let d;
	try {
		d = await api('GET', BASE + 'done');
	} catch (err) {
		frame('done', card('Not finished', null, notice('bad', err.message), nav('install', null)));
		return;
	}
	const link = (t, u, testid) => h('a', { class: 'big-link', href: u, target: '_blank', rel: 'noopener', 'data-testid': testid }, h('span', { class: 't' }, t), h('span', { class: 'u' }, u));
	let closed = false;
	const closeBtn = button(
		'Close setup',
		async () => {
			closeBtn.disabled = true;
			await api('POST', BASE + 'finish', {}).catch(() => {});
			closed = true;
			put(closeNote, notice('ok', 'Setup is switched off. To change something later, use the admin panel or run install.sh again.'));
		},
		{ kind: 'primary', testid: 'finish' },
	);
	const closeNote = h('div', { 'aria-live': 'polite' });
	frame(
		'done',
		card(
			'Boson is running',
			`Sign in as ${d.admin} in the web client or the admin panel.`,
			h(
					'div',
					{ class: 'stack' },
					link('Web client', d.links.web, 'link-web'),
					link('Admin panel', d.links.admin, 'link-admin'),
					d.mode === 'addon'
						? h('p', { class: 'muted', 'data-testid': 'done-irc' }, 'Boson’s desktop and phone apps: ', h('code', {}, d.links.irc), ' (the bridge, TLS). Classic IRC clients keep using ', h('code', {}, d.links.their_irc), '.')
						: h('p', { class: 'muted' }, 'Desktop and phone apps and any IRC client: ', h('code', {}, d.links.irc), ' (TLS).'),
				),
				d.mode === 'addon' && d.accounts === 'nickserv' ? notice('info', `Sign in with your NickServ account (${d.admin}) and its NickServ password.`) : null,
			d.oper_password
				? h(
						'div',
						{ class: 'panel' },
						h('h2', {}, 'Emergency IRC operator password'),
						h('p', { class: 'muted' }, 'For /OPER admin <password> if accounts ever stop working. It’s shown only now; only its hash is stored.'),
						h('div', { class: 'secret', 'data-testid': 'oper-password' }, d.oper_password),
					)
				: null,
			notice('info', 'This wizard switches itself off when you close it (or in 30 minutes). Your settings live in ', h('code', {}, `${d.dir}/.env`), ' and ', h('code', {}, `${d.dir}/config/boson.yaml`), '.'),
			closeNote,
			h('div', { class: 'actions' }, h('span'), h('div', { class: 'right' }, closeBtn)),
		),
	);
	void closed;
}
