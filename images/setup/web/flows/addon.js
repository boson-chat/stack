// "Add Boson to my existing IRC server": a stub until the IRC server probe
// (images/setup/probe, branch feat/setup-probe) is wired in. The flow's
// pages will go here, with the same mount(ctx) shape as fresh.js.
import { h, api, notice } from '../lib.js';

export async function mount({ shell, focusHeading }) {
	document.title = 'Add Boson to an IRC server · Boson setup';
	let message = 'This path isn’t ready yet.';
	try {
		message = (await api('GET', '/api/flows/addon/status')).message;
	} catch {
		/* keep the default */
	}
	shell(
		h(
			'section',
			{ class: 'card' },
			h('header', {}, h('h1', {}, 'Add Boson to my existing IRC server'), h('p', {}, 'Keep the IRC server you run today and add Boson’s accounts, voice, web client and admin panel next to it.')),
			notice('info', message),
			h('p', { class: 'muted' }, 'Until then you can set Boson up by hand next to your server: see the README’s Profiles section (for example COMPOSE_PROFILES=voice,auth,metrics).'),
			h('div', { class: 'actions' }, h('a', { class: 'btn', href: '#/' }, '← Back')),
		),
	);
	focusHeading();
}
