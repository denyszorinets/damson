<script lang="ts">
	import { resolve } from '$app/paths';
	import { arrowNav } from '$lib/keys';
	import { atLeast } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';
	import { isUser } from '$lib/session';

	/**
	 * The project's own sidebar; every page behind it shows only that project's
	 * data. Secrets need editor (who may list names), Settings and Access admin; the
	 * caller's role decides which tabs show, and the server checks again. Under 640 px
	 * it is a drawer behind a Menu button.
	 */
	let { path, tab, role }: { path: string; tab: string; role?: string } = $props();

	let open = $state(false);

	const items = $derived(
		[
			{ id: 'overview', label: 'Overview', rest: ['overview'], need: 'viewer' as const },
			{ id: '', label: 'Jobs', rest: [] as string[], need: 'viewer' as const },
			{ id: 'activity', label: 'Activity', rest: ['activity'], need: 'viewer' as const },
			{ id: 'schedules', label: 'Schedules', rest: ['schedules'], need: 'viewer' as const },
			{ id: 'webhooks', label: 'Webhooks', rest: ['webhooks'], need: 'viewer' as const },
			{ id: 'secrets', label: 'Secrets', rest: ['secrets'], need: 'editor' as const },
			{ id: 'access', label: 'Access', rest: ['access'], need: 'admin' as const },
			{ id: 'settings', label: 'Settings', rest: ['settings'], need: 'admin' as const }
		].filter((i) => {
			// A bearer token has no session for the project tabs of triggers.
			if (i.id === 'schedules' || i.id === 'webhooks') return isUser();
			return i.need === 'viewer' || atLeast(role, i.need);
		})
	);
	const label = $derived(items.find((i) => i.id === tab)?.label ?? 'Menu');
</script>

<div class="space-y-1">
	<button
		type="button"
		class="flex w-full items-center justify-between rounded border px-3 py-1.5 text-sm sm:hidden"
		aria-label="Project menu"
		aria-expanded={open}
		aria-controls="project-nav-list"
		onclick={() => (open = !open)}
		data-testid="nav-toggle"
	>
		<span>{label}</span><span aria-hidden="true">{open ? '▴' : '▾'}</span>
	</button>
	<nav
		id="project-nav-list"
		aria-label="Project"
		class="flex-col gap-1 sm:flex"
		class:hidden={!open}
		class:flex={open}
		use:arrowNav
		data-testid="project-nav"
	>
		{#each items as item (item.id)}
			<a
				class="rounded px-3 py-1.5 text-sm hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
				class:bg-muted={tab === item.id}
				class:font-medium={tab === item.id}
				aria-current={tab === item.id ? 'page' : undefined}
				href={resolve('/projects/[...path]', { path: projectRest(path, ...item.rest) })}
				onclick={() => (open = false)}
				data-testid="nav-{item.id || 'jobs'}">{item.label}</a
			>
		{/each}
	</nav>
</div>
