<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import RunList from '$lib/components/RunList.svelte';
	import { poll } from '$lib/polling';
	import { problemMessage, type Job } from '$lib/jobs';
	import { fetchProjectRuns } from '$lib/projectruns';
	import type { RunSummary } from '$lib/runs';

	/** The newest runs of one job. */
	let { job }: { job: Job } = $props();

	let runs = $state<RunSummary[]>([]);
	let next = $state('');
	let loaded = $state(false);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);
	const jobs = $derived(new Map([[job.id, job]]));

	onMount(() =>
		poll(async (signal) => {
			try {
				const page = await fetchProjectRuns(job.project, { job: job.id, limit: 25 }, signal);
				if (signal.aborted) return;
				runs = page.items;
				next = page.next;
				error = null;
			} catch (e) {
				if (!signal.aborted) error = problemMessage(e);
			} finally {
				if (!signal.aborted) loaded = true;
			}
		}, 10000)
	);

	async function more() {
		loadingMore = true;
		try {
			const page = await fetchProjectRuns(job.project, { job: job.id, cursor: next });
			runs = [...runs, ...page.items];
			next = page.next;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}
</script>

<div class="space-y-4" data-testid="job-runs">
	{#if error}
		<Alert.Root variant="destructive" data-testid="job-runs-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else if runs.length}
		<RunList {runs} {jobs} />
		{#if next}
			<Button variant="outline" onclick={more} disabled={loadingMore} data-testid="job-runs-more"
				>More…</Button
			>
		{/if}
	{:else}
		<p class="text-sm text-muted-foreground" data-testid="no-runs">No runs yet.</p>
	{/if}
</div>
