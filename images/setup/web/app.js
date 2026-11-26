// The wizard shell: the setup code, then the flow chooser, then a flow's
// pages (web/flows/<id>.js, each exporting mount(root, ctx)).
import { h, api, ApiError, field, notice, button, logo } from './lib.js';

const root = document.getElementById('app');

function shell(content, { wide = false } = {}) {
	root.replaceChildren(
		h('header', { class: 'top' }, h('a', { class: 'brand', href: '#/', 'aria-label': 'Boson setup, start' }, logo(20), 'Boson', h('span', { class: 'sub' }, 'Setup'))),
		h('main', { class: 'layout' + (wide ? '' : ' single'), id: 'main' }, content),
	);
}

function focusHeading() {
	const el = root.querySelector('h1');
	if (el) {
		el.setAttribute('tabindex', '-1');
		el.focus({ preventScroll: false });
	}
}

async function codeScreen(message) {
	document.title = 'Enter the setup code · Boson setup';
	let error = message || '';
	let busy = false;
	const render = () => {
		const f = field({ label: 'Setup code', name: 'code', placeholder: 'XXXX-XXXX', autocomplete: 'one-time-code', hint: 'install.sh printed it in your terminal, next to this address.', errors: error ? { code: error } : {} });
		const input = f.querySelector('input');
		input.classList.add('code-input');
		input.maxLength = 12;
		const form = h(
			'form',
			{
				class: 'card',
				onsubmit: async (e) => {
					e.preventDefault();
					busy = true;
					error = '';
					const code = input.value;
					try {
						await api('POST', '/api/session', { code });
						start();
					} catch (err) {
						busy = false;
						error = err instanceof ApiError ? err.message : String(err);
						render();
						root.querySelector('input[name=code]').value = code;
						root.querySelector('input[name=code]').focus();
					}
				},
			},
			h('header', {}, h('h1', {}, 'Set up your Boson server'), h('p', {}, 'Enter the one-time code to continue. It works in one browser only; for a new one, run install.sh again.')),
			f,
			h('div', { class: 'actions' }, h('div', { class: 'right' }, button('Continue', null, { kind: 'primary', type: 'submit', busy, testid: 'code-submit' }))),
		);
		shell(form);
		input.focus();
	};
	render();
}

async function chooser(flows) {
	document.title = 'Boson setup';
	shell(
		h(
			'section',
			{ class: 'stack' },
			h('header', { class: 'stack' }, h('h1', {}, 'What are you setting up?'), h('p', { class: 'muted' }, 'You can come back to this screen until the install starts.')),
			h(
				'div',
				{ class: 'flows' },
				flows.map((f) =>
					h(
						'button',
						{ class: 'flow-card', type: 'button', 'data-flow': f.id, onclick: () => (location.hash = `#/${f.id}`) },
						h('h2', {}, f.title, f.available ? null : h('span', { class: 'tag' }, 'Coming soon')),
						h('p', {}, f.summary),
					),
				),
			),
		),
	);
	focusHeading();
}

let flows = [];

async function route() {
	const [, id, step] = location.hash.match(/^#\/([a-z-]+)?\/?([a-z-]+)?/) || [];
	if (!id) return chooser(flows);
	const f = flows.find((x) => x.id === id);
	if (!f) return chooser(flows);
	const mod = await import(`./flows/${id}.js`);
	await mod.mount({ root, shell, focusHeading, flow: f, step });
}

async function start() {
	try {
		const s = await api('GET', '/api/session');
		if (s.closed) {
			shell(h('div', { class: 'card' }, h('h1', {}, 'Setup is finished'), notice('info', 'This setup wizard has switched itself off. To change something, use the admin panel, or run install.sh again for a new code.')));
			return;
		}
		if (!s.ok) return codeScreen();
		flows = (await api('GET', '/api/flows')).flows;
		await route();
	} catch (err) {
		shell(h('div', { class: 'card' }, h('h1', {}, 'Setup is not reachable'), notice('bad', String(err.message || err))));
	}
}

window.addEventListener('hashchange', () => route().catch((e) => console.error(e)));
window.addEventListener('setup:signed-out', () => codeScreen('Your session ended. Enter the code again (run install.sh again for a new one if it was used).'));
start();
