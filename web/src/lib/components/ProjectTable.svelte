<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { formatDuration, formatWhen } from '$lib/format';
	import { problemMessage } from '$lib/jobs';
	import { poll } from '$lib/polling';
	import { atLeast, fetchProject } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';
	import { fetchLevel, type TreeJob, type TreeProject } from '$lib/tree';

	/**
	 * A project as a folder: its child projects and its own jobs in one table,
	 * each job with how it last ran. The server builds the level, so nothing the
	 * caller may not see is listed or counted.
	 */
	let { path }: { path: string } = $props();

	const REFRESH_MS = 10000;

	let projects = $state<TreeProject[]>([]);
	let jobs = $state<TreeJob[]>([]);
	let next = $state('');
	let jobsNext = $state('');
	let loaded = $state(false);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);
	let myRole = $state<string | undefined>(undefined);

	onMount(() => {
		void fetchProject(path).then((p) => (myRole = p?.my_role));
		return poll(async (signal) => {
			try {
				const level = await fetchLevel(path);
				if (signal.aborted) return;
				projects = level.projects;
				jobs = level.jobs;
				next = level.next;
				jobsNext = level.jobsNext;
				error = null;
			} catch (e) {
				if (!signal.aborted) error = problemMessage(e);
			} finally {
				if (!signal.aborted) loaded = true;
			}
		}, REFRESH_MS);
	});

	async function more() {
		loadingMore = true;
		try {
			const level = await fetchLevel(path, next, jobsNext);
			projects = [...projects, ...level.projects];
			jobs = [...jobs, ...level.jobs];
			next = level.next;
			jobsNext = level.jobsNext;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}

	const variant = (status: string | undefined) =>
		status === 'failed' ? 'destructive' : status === 'succeeded' ? 'default' : 'secondary';
</script>

<div class="space-y-6">
	<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>

	{#if atLeast(myRole, 'editor')}
		<a
			class="inline-block rounded-md border px-3 py-1 text-sm hover:bg-muted"
			href={resolve('/jobs/new/[...project]', { project: path })}
			data-testid="new-job">New job in {path}</a
		>
	{/if}

	{#if error}
		<Alert.Root variant="destructive" data-testid="jobs-error">
			<Alert.Description>
				Not found, or you may not have access. <a class="underline" href={resolve('/')}
					>Back to projects</a
				>.
			</Alert.Description>
		</Alert.Root>
	{:else if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else}
		<Table.Root data-testid="jobs-table">
			<Table.Header>
				<Table.Row>
					<Table.Head>Name</Table.Head>
					<Table.Head>Status</Table.Head>
					<Table.Head>Last success</Table.Head>
					<Table.Head>Last failure</Table.Head>
					<Table.Head>Duration</Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each projects as p (p.path)}
					<Table.Row data-testid="project-row">
						<Table.Cell>
							<a
								class="font-medium underline-offset-4 hover:underline"
								href={resolve('/projects/[...path]', { path: projectRest(p.path) })}
								data-testid="project-link-{p.path}">{p.label}/</a
							>
							<span class="ml-2 text-xs text-muted-foreground">{p.jobs ?? 0} jobs</span>
						</Table.Cell>
						<Table.Cell>
							{#if p.failed_24h}
								<Badge variant="destructive">{p.failed_24h} failed in 24 h</Badge>
							{:else if p.running}
								<Badge variant="secondary">{p.running} running</Badge>
							{:else}
								-
							{/if}
						</Table.Cell>
						<Table.Cell>-</Table.Cell>
						<Table.Cell>-</Table.Cell>
						<Table.Cell>-</Table.Cell>
					</Table.Row>
				{/each}
				{#each jobs as j (j.id)}
					<Table.Row data-testid="job-row">
						<Table.Cell>
							<a
								class="font-medium underline-offset-4 hover:underline"
								href={resolve('/jobs/[id]', { id: j.id })}
								data-testid="job-link-{j.name}">{j.name}</a
							>
						</Table.Cell>
						<Table.Cell>
							{#if j.last_status}
								<Badge variant={variant(j.last_status)} data-testid="job-status-{j.name}"
									>{j.last_status}</Badge
								>
							{:else}
								<span class="text-muted-foreground" data-testid="job-status-{j.name}"
									>never run</span
								>
							{/if}
						</Table.Cell>
						<Table.Cell data-testid="job-success-{j.name}"
							>{formatWhen(j.last_success_at)}</Table.Cell
						>
						<Table.Cell data-testid="job-failure-{j.name}"
							>{formatWhen(j.last_failure_at)}</Table.Cell
						>
						<Table.Cell data-testid="job-duration-{j.name}"
							>{formatDuration(j.last_duration_ms)}</Table.Cell
						>
					</Table.Row>
				{/each}
				{#if !projects.length && !jobs.length}
					<Table.Row>
						<Table.Cell colspan={5} class="text-muted-foreground">
							No child projects or jobs yet.
						</Table.Cell>
					</Table.Row>
				{/if}
			</Table.Body>
		</Table.Root>
		{#if next || jobsNext}
			<Button variant="outline" onclick={more} disabled={loadingMore} data-testid="jobs-more"
				>More…</Button
			>
		{/if}
	{/if}
</div>
