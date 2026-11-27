<script>
	// boson's orbital-electron mark, in currentColor.
	let { size = 18 } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
	<circle cx="12" cy="12" r="2" fill="currentColor" />
	<ellipse cx="12" cy="12" rx="10" ry="4" />
	<ellipse cx="12" cy="12" rx="10" ry="4" transform="rotate(60 12 12)" />
	<ellipse cx="12" cy="12" rx="10" ry="4" transform="rotate(120 12 12)" />
</svg>
