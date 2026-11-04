<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import GraphView from '$lib/graph/GraphView.svelte';
	import type { Graph } from '$lib/graph/layout';
	import { fetchJobGraph, problemMessage, type Job } from '$lib/jobs';

	let job = $state<Job | null>(null);
	let graph = $state<Graph | null>(null);
	let error = $state<string | null>(null);

	$effect(() => {
		const id = page.params.id ?? '';
		const ctrl = new AbortController();
		job = graph = null;
		error = null;
		fetchJobGraph(id, ctrl.signal)
			.then((r) => {
				if (ctrl.signal.aborted) return;
				job = r.job;
				graph = r.graph;
			})
			.catch((e) => {
				if (!ctrl.signal.aborted) error = problemMessage(e);
			});
		return () => ctrl.abort();
	});
</script>

<svelte:head>
	<title>{job ? job.name : 'Job'} - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-baseline gap-3">
		<a class="text-sm text-muted-foreground hover:text-foreground" href={resolve('/jobs')}>Jobs</a>
		<h1 class="text-2xl font-semibold" data-testid="job-title">{job?.name ?? ''}</h1>
		{#if job}<span class="text-sm text-muted-foreground">version {job.current_version}</span>{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="job-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !graph}
		<Skeleton class="h-64 w-full" />
	{:else}
		<GraphView {graph} />
	{/if}
</main>
