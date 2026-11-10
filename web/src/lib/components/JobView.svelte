<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Badge } from '$lib/components/ui/badge';
	import GraphView from '$lib/graph/GraphView.svelte';
	import type { Graph } from '$lib/graph/layout';
	import { fetchJobGraph, isNotFound, problemMessage, type Job } from '$lib/jobs';
	import { jobRest, jobTab } from '$lib/jobroute';
	import NotFound from '$lib/components/NotFound.svelte';
	import { atLeast, isInstanceAdmin } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';
	import JobRuns from '$lib/components/JobRuns.svelte';
	import JobTriggers from '$lib/components/JobTriggers.svelte';
	import RunDialog from '$lib/components/RunDialog.svelte';
	import VersionHistory from '$lib/components/VersionHistory.svelte';
	import { optionsSchema } from '$lib/triggers';

	/**
	 * A job under its project: Run, Runs, Definition (graph and versions), Schedule
	 * and Webhooks tabs. The job's own project is the one in the URL; a URL with the
	 * wrong project is sent to the right one.
	 */
	let { path, id, sub }: { path: string; id: string; sub: string[] } = $props();

	let job = $state<Job | null>(null);
	let graph = $state<Graph | null>(null);
	let schema = $state<Record<string, unknown> | null>(null);
	let error = $state<string | null>(null);
	let missing = $state(false);

	const tab = $derived(jobTab(sub));
	// Schedules and webhooks are shown to instance admins here; the project tabs of R13 show them by role.
	const triggers = $derived(isInstanceAdmin());
	const tabs = $derived([
		{ id: '', label: 'Run' },
		{ id: 'runs', label: 'Runs' },
		{ id: 'definition', label: 'Definition' },
		...(triggers
			? [
					{ id: 'schedule', label: 'Schedule' },
					{ id: 'webhooks', label: 'Webhooks' }
				]
			: [])
	]);

	$effect(() => {
		const jobId = id;
		const ctrl = new AbortController();
		job = graph = schema = null;
		error = null;
		missing = false;
		fetchJobGraph(jobId, ctrl.signal)
			.then((r) => {
				if (ctrl.signal.aborted) return;
				if (r.job.project !== path) {
					// The job lives elsewhere: the canonical URL carries its own project.
					void goto(
						resolve('/projects/[...path]', { path: jobRest(r.job.project, jobId, ...sub) }),
						{ replaceState: true }
					);
					return;
				}
				job = r.job;
				graph = r.graph;
				schema = optionsSchema(r.spec);
			})
			.catch((e) => {
				if (ctrl.signal.aborted) return;
				if (isNotFound(e)) missing = true;
				else error = problemMessage(e);
			});
		return () => ctrl.abort();
	});

	const here = (t: string) =>
		resolve('/projects/[...path]', { path: jobRest(path, id, ...(t ? [t] : [])) });
</script>

<svelte:head>
	<title>{job ? job.name : 'Job'} - Damson</title>
</svelte:head>

<div class="space-y-6">
	<div class="flex items-baseline gap-3">
		<a
			class="text-sm text-muted-foreground hover:text-foreground"
			href={resolve('/projects/[...path]', { path: projectRest(path) })}>{path}</a
		>
		<h1 class="text-2xl font-semibold" data-testid="job-title">{job?.name ?? ''}</h1>
		{#if job}<span class="text-sm text-muted-foreground">version {job.current_version}</span>{/if}
		{#if job && job.managed_by !== 'ui'}
			<Badge variant="secondary" data-testid="job-managed"
				>managed by {job.managed_by}; change it there</Badge
			>
		{/if}
		{#if job?.archived}<Badge variant="outline" data-testid="job-archived">archived</Badge>{/if}
		{#if job && atLeast(job.my_role, 'editor')}
			<a
				class="ml-auto text-sm underline underline-offset-4"
				href={resolve('/jobs/[id]/edit', { id: job.id })}
				data-testid="edit-job">{job.managed_by === 'ui' && !job.archived ? 'Edit' : 'View spec'}</a
			>
		{/if}
	</div>

	{#if missing || tab === null}
		<NotFound what="This job" />
	{:else if error}
		<Alert.Root variant="destructive" data-testid="job-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !job || !graph}
		<Skeleton class="h-64 w-full" />
	{:else}
		<nav aria-label="Job" class="flex flex-wrap gap-1 border-b" data-testid="job-tabs">
			{#each tabs as t (t.id)}
				<a
					class="rounded-t px-3 py-1.5 text-sm hover:bg-muted"
					class:bg-muted={tab === t.id}
					class:font-medium={tab === t.id}
					aria-current={tab === t.id ? 'page' : undefined}
					href={here(t.id)}
					data-testid="job-tab-{t.id || 'run'}">{t.label}</a
				>
			{/each}
		</nav>
		{#if tab === ''}
			<RunDialog jobId={job.id} project={job.project} {schema} role={job.my_role} />
		{:else if tab === 'runs'}
			<JobRuns {job} />
		{:else if tab === 'definition'}
			<GraphView {graph} />
			<VersionHistory jobId={job.id} current={job.current_version} />
		{:else if triggers}
			<JobTriggers jobId={job.id} only={tab === 'schedule' ? 'schedules' : 'webhooks'} />
		{:else}
			<NotFound what="This page" />
		{/if}
	{/if}
</div>
