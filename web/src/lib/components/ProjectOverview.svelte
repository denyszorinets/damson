<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import RunList from '$lib/components/RunList.svelte';
	import { poll } from '$lib/polling';
	import { problemMessage, type Job } from '$lib/jobs';
	import { fetchProjectJobs, fetchProjectRuns, jobHealth, successRate } from '$lib/projectruns';
	import type { RunSummary } from '$lib/runs';

	/** What is running and waiting, recent runs and per-job health of the project and the projects below it. */
	let { path }: { path: string } = $props();

	const REFRESH_MS = 10000;
	const WINDOW = 50; // health and the success rate cover the newest runs, not all of history
	const SHOWN = 10;
	const ACTIVE = 20; // running and waiting are queried on their own so recent runs cannot crowd them out

	let runs = $state<RunSummary[]>([]);
	let running = $state<RunSummary[]>([]);
	let waiting = $state<RunSummary[]>([]);
	let jobs = $state(new Map<string, Job>());
	let loaded = $state(false);
	let error = $state<string | null>(null);

	const rate = $derived(successRate(runs));
	const finished = $derived(runs.filter((r) => r.status === 'succeeded' || r.status === 'failed'));
	const health = $derived(jobHealth(runs, jobs));
	const jobName = (id: string) => jobs.get(id)?.name ?? id;

	onMount(() =>
		poll(async (signal) => {
			try {
				const [page, run, wait, byId] = await Promise.all([
					fetchProjectRuns(path, { limit: WINDOW }, signal),
					fetchProjectRuns(path, { status: 'running', limit: ACTIVE }, signal),
					fetchProjectRuns(path, { status: 'pending', limit: ACTIVE }, signal),
					fetchProjectJobs(path, signal)
				]);
				if (signal.aborted) return;
				runs = page.items;
				running = run.items;
				waiting = wait.items;
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
		<div class="grid gap-6 md:grid-cols-2">
			<section class="space-y-2" aria-label="Running now" data-testid="running-now">
				<h2 class="text-lg font-medium">Running now</h2>
				{#if running.length}
					<RunList runs={running} {jobs} />
				{:else}
					<p class="text-sm text-muted-foreground">Nothing is running.</p>
				{/if}
			</section>
			<section class="space-y-2" aria-label="Waiting" data-testid="waiting">
				<h2 class="text-lg font-medium">Waiting</h2>
				{#if waiting.length}
					<RunList runs={waiting} {jobs} />
				{:else}
					<p class="text-sm text-muted-foreground">Nothing is waiting.</p>
				{/if}
			</section>
		</div>
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
		<section class="space-y-2" aria-label="Job health" data-testid="job-health">
			<h2 class="text-lg font-medium">Job health</h2>
			{#if health.length}
				<ul class="divide-y rounded-md border text-sm">
					{#each health as h (h.jobId)}
						<li class="flex items-center gap-3 px-3 py-2" data-testid="job-health-row">
							<Badge
								variant={h.last === 'failed' ? 'destructive' : 'secondary'}
								data-testid="job-health-last"
								>{h.last ?? (h.runs ? 'in progress' : `none in last ${WINDOW}`)}</Badge
							>
							<span class="flex-1 truncate">{jobName(h.jobId)}</span>
							<span class="text-xs text-muted-foreground" data-testid="job-health-rate"
								>{h.rate === null ? '–' : `${h.rate}%`}</span
							>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="text-sm text-muted-foreground">No jobs yet.</p>
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
