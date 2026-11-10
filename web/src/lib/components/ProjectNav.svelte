<script lang="ts">
	import { resolve } from '$app/paths';
	import { projectRest } from '$lib/projectroute';

	/** The project's own sidebar; every page behind it shows only that project's data. */
	let { path, tab }: { path: string; tab: string } = $props();

	const items = [
		{ id: 'overview', label: 'Overview', rest: ['overview'] },
		{ id: '', label: 'Jobs', rest: [] },
		{ id: 'activity', label: 'Activity', rest: ['activity'] }
	];
</script>

<nav aria-label="Project" class="flex gap-1 md:flex-col" data-testid="project-nav">
	{#each items as item (item.id)}
		<a
			class="rounded px-3 py-1.5 text-sm hover:bg-muted"
			class:bg-muted={tab === item.id}
			class:font-medium={tab === item.id}
			aria-current={tab === item.id ? 'page' : undefined}
			href={resolve('/projects/[...path]', { path: projectRest(path, ...item.rest) })}
			data-testid="nav-{item.id || 'jobs'}">{item.label}</a
		>
	{/each}
</nav>
