<script lang="ts">
	import { page } from '$app/state';
	import JobsView from '$lib/components/JobsView.svelte';
	import NotFound from '$lib/components/NotFound.svelte';
	import { parseProjectRoute } from '$lib/projectroute';

	// The URL is the project's path, then "/-/" and a tab; a view is stable and shareable.
	const route = $derived(parseProjectRoute(page.params.path));
</script>

<svelte:head>
	<title>{route.path} - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl px-4 py-8">
	{#if route.tab.length}
		<NotFound />
	{:else}
		<JobsView scope={route.path} />
	{/if}
</main>
