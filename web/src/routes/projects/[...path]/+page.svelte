<script lang="ts">
	import { page } from '$app/state';
	import JobsView from '$lib/components/JobsView.svelte';

	// The URL is the project's path, so a view is stable and shareable.
	const scope = $derived(page.params.path ?? '');
</script>

<svelte:head>
	<title>{scope} - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl px-4 py-8">
	<JobsView {scope} />
</main>
