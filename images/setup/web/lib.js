// Small helpers for the wizard's pages: an element builder, the API client
// and the admin panel's components (Toggle, ChoiceGroup, Notice, fields).

/** h('div', {class: 'x', onclick}, ...children): one DOM element. */
export function h(tag, attrs, ...children) {
	const el = document.createElement(tag);
	for (const [k, v] of Object.entries(attrs || {})) {
		if (v === undefined || v === null || v === false) continue;
		if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2), v);
		else if (k === 'class') el.className = v;
		else if (k === 'html') throw new Error('no html');
		else if (k in el && typeof v !== 'string') el[k] = v;
		else el.setAttribute(k, v === true ? '' : String(v));
	}
	for (const c of children.flat(Infinity)) {
		if (c === null || c === undefined || c === false) continue;
		el.append(c instanceof Node ? c : document.createTextNode(String(c)));
	}
	return el;
}

/** el.replaceChildren without the null/false holes conditionals leave. */
export function put(el, ...children) {
	el.replaceChildren(...children.flat(Infinity).filter((c) => c !== null && c !== undefined && c !== false));
}

export class ApiError extends Error {
	constructor(status, body) {
		super((body && body.error) || `HTTP ${status}`);
		this.status = status;
		this.body = body || {};
		this.code = this.body.code;
	}
}

/** api('GET', '/api/flows'): JSON in and out, the session cookie included. */
export async function api(method, path, body) {
	const res = await fetch(path, {
		method,
		credentials: 'same-origin',
		headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
		body: body === undefined ? undefined : JSON.stringify(body),
	});
	let data = null;
	try {
		data = await res.json();
	} catch {
		/* empty body */
	}
	if (!res.ok) {
		if (res.status === 401 && data && data.code === 'no_session') window.dispatchEvent(new Event('setup:signed-out'));
		throw new ApiError(res.status, data);
	}
	return data;
}

let uid = 0;
const nextId = (p) => `${p}-${++uid}`;

/** A labelled input (admin .field). errors: {field: message}. */
export function field({ label, name, value = '', type = 'text', hint, errors = {}, autocomplete, placeholder, oninput, inputmode, required }) {
	const id = nextId('f');
	const err = errors[name];
	const hintId = hint ? id + '-hint' : undefined;
	const errId = err ? id + '-err' : undefined;
	const input = h('input', {
		id,
		name,
		type,
		value,
		autocomplete: autocomplete || 'off',
		placeholder,
		inputmode,
		required,
		spellcheck: 'false',
		autocapitalize: 'off',
		'aria-invalid': err ? 'true' : undefined,
		'aria-describedby': [hintId, errId].filter(Boolean).join(' ') || undefined,
		oninput,
	});
	return h(
		'div',
		{ class: 'field' },
		h('label', { for: id }, label),
		input,
		hint ? h('small', { id: hintId }, hint) : null,
		err ? h('span', { class: 'err', id: errId, role: 'alert' }, err) : null,
	);
}

/** A select with options [[value, label]]. */
export function select({ label, name, value, options, onchange, hint }) {
	const id = nextId('s');
	return h(
		'div',
		{ class: 'field' },
		h('label', { for: id }, label),
		h('select', { id, name, onchange }, options.map(([v, l]) => h('option', { value: v, selected: v === value }, l))),
		hint ? h('small', {}, hint) : null,
	);
}

/** An on/off switch (admin Toggle.svelte). */
export function toggle({ label, detail, checked, onchange, disabled, name }) {
	const input = h('input', { type: 'checkbox', role: 'switch', name, checked: !!checked, disabled: !!disabled });
	input.addEventListener('change', () => onchange && onchange(input.checked));
	return h(
		'label',
		{ class: 'toggle' + (disabled ? ' disabled' : '') },
		h('span', { class: 'txt' }, h('span', { class: 'label' }, label), detail ? h('span', { class: 'detail' }, detail) : null),
		input,
		h('span', { class: 'track', 'aria-hidden': 'true' }, h('span', { class: 'knob' })),
	);
}

/** Radio rows that say what each choice means (admin ChoiceGroup.svelte). */
export function choices({ legend, hint, name, value, options, onchange }) {
	const rows = options.map((o) => {
		const input = h('input', { type: 'radio', name, value: o.value, checked: o.value === value });
		const row = h(
			'label',
			{ class: 'choice' + (o.value === value ? ' on' : '') },
			input,
			h('span', { class: 'mark', 'aria-hidden': 'true' }),
			h('span', { class: 'ctxt' }, h('span', { class: 'clabel' }, o.label), h('span', { class: 'cdetail' }, o.detail)),
		);
		input.addEventListener('change', () => {
			for (const r of rows) r.classList.toggle('on', r.querySelector('input').checked);
			onchange && onchange(o.value);
		});
		return row;
	});
	return h('fieldset', { class: 'group' }, h('legend', {}, legend), hint ? h('p', { class: 'hint' }, hint) : null, h('div', { class: 'choices' }, rows));
}

/** A notice (admin Notice.svelte): tone bad | warn | info | ok. */
export function notice(tone, ...children) {
	// Text and inline elements run together in one paragraph; blocks (pre,
	// lists, button rows) stand on their own rows of the grid.
	const out = [];
	let para = null;
	for (const c of children.flat(Infinity)) {
		if (c === null || c === undefined || c === false) continue;
		const block = c instanceof HTMLElement && /^(PRE|DIV|UL|OL|P|DETAILS)$/.test(c.tagName);
		if (block) {
			out.push(c);
			para = null;
		} else {
			if (!para) out.push((para = h('p', {})));
			para.append(c instanceof Node ? c : document.createTextNode(String(c)));
		}
	}
	return h('div', { class: `notice ${tone}`, role: tone === 'bad' ? 'alert' : 'status' }, out);
}

export function tag(tone, text) {
	return h('span', { class: `tag tag-${tone}` }, text);
}

export function button(label, onclick, { kind = '', type = 'button', disabled, busy, testid } = {}) {
	return h(
		'button',
		{ class: `btn ${kind ? 'btn-' + kind : ''}`, type, onclick, disabled: !!disabled || !!busy, 'data-testid': testid, 'aria-busy': busy ? 'true' : undefined },
		busy ? h('span', { class: 'spinner', 'aria-hidden': 'true' }) : null,
		label,
	);
}

/** boson's orbital-electron mark (admin Logo.svelte). */
export function logo(size = 20) {
	const ns = 'http://www.w3.org/2000/svg';
	const svg = document.createElementNS(ns, 'svg');
	for (const [k, v] of Object.entries({ width: size, height: size, viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'aria-hidden': 'true' })) svg.setAttribute(k, v);
	const add = (t, a) => {
		const e = document.createElementNS(ns, t);
		for (const [k, v] of Object.entries(a)) e.setAttribute(k, v);
		svg.append(e);
	};
	add('circle', { cx: 12, cy: 12, r: 2, fill: 'currentColor' });
	for (const r of [0, 60, 120]) add('ellipse', { cx: 12, cy: 12, rx: 10, ry: 4, transform: `rotate(${r} 12 12)` });
	return svg;
}

/** Field errors list [{field, message}] → {field: message}. */
export function errorMap(err) {
	const out = {};
	for (const e of (err && err.body && err.body.errors) || []) out[e.field] = e.message;
	return out;
}
