<script lang="ts">
	import { page } from '$app/state';
	import JobsView from '$lib/components/JobsView.svelte';
	import NotFound from '$lib/components/NotFound.svelte';
	import ProjectActivity from '$lib/components/ProjectActivity.svelte';
	import ProjectNav from '$lib/components/ProjectNav.svelte';
	import ProjectOverview from '$lib/components/ProjectOverview.svelte';
	import ProjectTable from '$lib/components/ProjectTable.svelte';
	import { isUser } from '$lib/session';
	import { parseProjectRoute } from '$lib/projectroute';

	// The URL is the project's path, then "/-/" and a tab; a view is stable and shareable.
	const route = $derived(parseProjectRoute(page.params.path));
	// The front page is the jobs table; the other tabs are named after the "-".
	const tab = $derived(route.tab.length ? route.tab[0] : '');
	const known = $derived(route.tab.length <= 1 && ['', 'overview', 'activity'].includes(tab));
</script>

<svelte:head>
	<title>{route.path} - Damson</title>
</svelte:head>

<main class="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-8 md:flex-row">
	{#if known}
		<aside class="shrink-0 md:w-48">
			<ProjectNav path={route.path} {tab} />
		</aside>
		<div class="min-w-0 flex-1">
			{#key route.path}
				{#if tab === 'overview'}
					<ProjectOverview path={route.path} />
				{:else if tab === 'activity'}
					<ProjectActivity path={route.path} />
				{:else if isUser()}
					<ProjectTable path={route.path} />
				{:else}
					<!-- A bearer token has no session for the tree: the flat list of the project and below. -->
					<JobsView scope={route.path} />
				{/if}
			{/key}
		</div>
	{:else}
		<div class="min-w-0 flex-1"><NotFound /></div>
	{/if}
</main>
