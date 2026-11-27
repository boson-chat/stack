// A tiny history router: the server answers every path with index.html.
export const route = $state({ path: location.pathname, search: location.search });

export function navigate(href, replace = false) {
	const u = new URL(href, location.href);
	if (replace) history.replaceState({}, '', u);
	else history.pushState({}, '', u);
	route.path = u.pathname;
	route.search = u.search;
	window.scrollTo(0, 0);
}

addEventListener('popstate', () => {
	route.path = location.pathname;
	route.search = location.search;
});

// Same-origin <a> clicks become navigations.
addEventListener('click', (e) => {
	if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
	const a = e.target instanceof Element ? e.target.closest('a[href]') : null;
	if (!a || a.target || a.hasAttribute('download')) return;
	const u = new URL(a.href);
	if (u.origin !== location.origin) return;
	e.preventDefault();
	navigate(u.pathname + u.search);
});
