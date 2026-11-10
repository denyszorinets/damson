<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import * as Select from '$lib/components/ui/select';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import RunList from '$lib/components/RunList.svelte';
	import { problemMessage, type Job } from '$lib/jobs';
	import {
		RUN_STATUSES,
		fetchProjectJobs,
		fetchProjectRuns,
		type RunStatus
	} from '$lib/projectruns';
	import type { RunSummary } from '$lib/runs';

	/** Runs of the project and the projects below it, filtered by status and job. */
	let { path }: { path: string } = $props();

	let status = $state<RunStatus | ''>('');
	let job = $state('');
	let runs = $state<RunSummary[]>([]);
	let next = $state('');
	let jobs = $state(new Map<string, Job>());
	let loaded = $state(false);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		const ctl = new AbortController();
		void fetchProjectJobs(path, ctl.signal).then(
			(m) => (jobs = m),
			() => {} // the run list shows the load error
		);
		return () => ctl.abort();
	});

	$effect(() => {
		const q = { status, job };
		const ctl = new AbortController();
		loaded = false;
		error = null;
		fetchProjectRuns(path, q, ctl.signal)
			.then((p) => {
				runs = p.items;
				next = p.next;
			})
			.catch((e) => {
				if (ctl.signal.aborted) return;
				runs = [];
				next = '';
				error = problemMessage(e);
			})
			.finally(() => {
				if (!ctl.signal.aborted) loaded = true;
			});
		return () => ctl.abort();
	});

	async function more() {
		loadingMore = true;
		const asked = { status, job };
		try {
			const p = await fetchProjectRuns(path, { ...asked, cursor: next });
			// The filters changed while this page was in flight: the list it belonged to is gone.
			if (asked.status !== status || asked.job !== job) return;
			runs = [...runs, ...p.items];
			next = p.next;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}
</script>

<div class="space-y-6" data-testid="activity">
	<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>
	<div class="flex flex-wrap items-center gap-3" role="group" aria-label="Filters">
		<Select.Root type="single" value={status} onValueChange={(v) => (status = v as RunStatus | '')}>
			<Select.Trigger class="w-40" aria-label="Status" data-testid="filter-status"
				>{status || 'Any status'}</Select.Trigger
			>
			<Select.Content>
				<Select.Item value="">Any status</Select.Item>
				{#each RUN_STATUSES as s (s)}
					<Select.Item value={s}>{s}</Select.Item>
				{/each}
			</Select.Content>
		</Select.Root>
		<Select.Root type="single" value={job} onValueChange={(v) => (job = v)}>
			<Select.Trigger class="w-56" aria-label="Job" data-testid="filter-job"
				>{job ? (jobs.get(job)?.name ?? job) : 'Any job'}</Select.Trigger
			>
			<Select.Content>
				<Select.Item value="">Any job</Select.Item>
				{#each jobs.values() as j (j.id)}
					<Select.Item value={j.id}>{j.name}</Select.Item>
				{/each}
			</Select.Content>
		</Select.Root>
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="activity-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !loaded}
		<Skeleton class="h-32 w-full" />
	{:else}
		{#if runs.length}
			<RunList {runs} {jobs} />
		{:else}
			<p class="text-sm text-muted-foreground" data-testid="no-runs">
				{next ? 'No runs among the latest ones; load more to look further.' : 'No runs.'}
			</p>
		{/if}
		{#if next}
			<Button variant="outline" onclick={more} disabled={loadingMore} data-testid="runs-more"
				>More…</Button
			>
		{/if}
	{/if}
</div>
