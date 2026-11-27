// The panel's API: same origin, the session cookie, X-Stats on every call
// (the server refuses state changes without it).
export class ApiError extends Error {
	constructor(status, message) {
		super(message);
		this.status = status;
	}
}

export const auth = $state({ user: null, checked: false });

export async function api(method, path, body) {
	const res = await fetch('/api' + path, {
		method,
		headers: { 'X-Stats': '1', ...(body ? { 'Content-Type': 'application/json' } : {}) },
		body: body ? JSON.stringify(body) : undefined,
		credentials: 'same-origin'
	});
	let data = null;
	try {
		data = await res.json();
	} catch {
		/* not JSON */
	}
	if (!res.ok) {
		if (res.status === 401 && path !== '/login') auth.user = null;
		throw new ApiError(res.status, (data && data.error) || res.statusText);
	}
	return data;
}
