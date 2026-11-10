<script lang="ts">
	import { resolve } from '$app/paths';
	import { atLeast } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';

	/**
	 * The project's own sidebar; every page behind it shows only that project's
	 * data. Secrets need editor (who may list names), Settings and Access admin; the
	 * caller's role decides which tabs show, and the server checks again.
	 */
	let { path, tab, role }: { path: string; tab: string; role?: string } = $props();

	const items = $derived(
		[
			{ id: 'overview', label: 'Overview', rest: ['overview'], need: 'viewer' as const },
			{ id: '', label: 'Jobs', rest: [] as string[], need: 'viewer' as const },
			{ id: 'activity', label: 'Activity', rest: ['activity'], need: 'viewer' as const },
			{ id: 'secrets', label: 'Secrets', rest: ['secrets'], need: 'editor' as const },
			{ id: 'access', label: 'Access', rest: ['access'], need: 'admin' as const },
			{ id: 'settings', label: 'Settings', rest: ['settings'], need: 'admin' as const }
		].filter((i) => i.need === 'viewer' || atLeast(role, i.need))
	);
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
