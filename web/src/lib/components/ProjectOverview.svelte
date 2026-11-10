<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import RunList from '$lib/components/RunList.svelte';
	import { poll } from '$lib/polling';
	import { problemMessage, type Job } from '$lib/jobs';
	import { fetchProjectJobs, fetchProjectRuns, successRate } from '$lib/projectruns';
	import type { RunSummary } from '$lib/runs';

	/** Recent runs and the success rate of the project and the projects below it. */
	let { path }: { path: string } = $props();

	const REFRESH_MS = 10000;
	const WINDOW = 50; // the success rate covers the newest runs, not all of history
	const SHOWN = 10;

	let runs = $state<RunSummary[]>([]);
	let jobs = $state(new Map<string, Job>());
	let loaded = $state(false);
	let error = $state<string | null>(null);

	const rate = $derived(successRate(runs));
	const finished = $derived(runs.filter((r) => r.status === 'succeeded' || r.status === 'failed'));

	onMount(() =>
		poll(async (signal) => {
			try {
				const [page, byId] = await Promise.all([
					fetchProjectRuns(path, { limit: WINDOW }, signal),
					fetchProjectJobs(path, signal)
				]);
				if (signal.aborted) return;
				runs = page.items;
				jobs = byId;
				error = null;
			} catch (e) {
				if (!signal.aborted) error = problemMessage(e);
			} finally {
				if (!signal.aborted) loaded = true;
			}
		}, REFRESH_MS)
	);
</script>

<div class="space-y-6" data-testid="overview">
	<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>
	{#if error}
		<Alert.Root variant="destructive" data-testid="overview-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !loaded}
		<Skeleton class="h-32 w-full" />
	{:else}
		<section class="space-y-1" aria-label="Success rate">
			<h2 class="text-lg font-medium">Success rate</h2>
			{#if rate === null}
				<p class="text-sm text-muted-foreground" data-testid="success-rate">
					No finished runs yet.
				</p>
			{:else}
				<p data-testid="success-rate">
					<span class="text-2xl font-semibold">{rate}%</span>
					<span class="text-sm text-muted-foreground"
						>of the last {finished.length} finished runs</span
					>
				</p>
			{/if}
		</section>
		<section class="space-y-2" aria-label="Recent runs">
			<h2 class="text-lg font-medium">Recent runs</h2>
			{#if runs.length}
				<RunList runs={runs.slice(0, SHOWN)} {jobs} />
			{:else}
				<p class="text-sm text-muted-foreground" data-testid="no-runs">No runs yet.</p>
			{/if}
		</section>
	{/if}
</div>
