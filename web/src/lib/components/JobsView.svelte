<script lang="ts">
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { jobRest } from '$lib/jobroute';
	import { fetchScopedJobs, problemMessage, type Job } from '$lib/jobs';
	import { atLeast, fetchProject } from '$lib/projects';

	/** The project whose jobs to list, with those below it. */
	let { scope = '' }: { scope?: string } = $props();

	let jobs = $state<Job[]>([]);
	let next = $state('');
	let loaded = $state(false);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);
	let myRole = $state<string | undefined>(undefined); // the caller's role on the scope project

	$effect(() => {
		const current = scope;
		const ctl = new AbortController();
		myRole = undefined;
		if (current) fetchProject(current).then((p) => current === scope && (myRole = p?.my_role));
		loaded = false;
		error = null;
		fetchScopedJobs(current, '', ctl.signal)
			.then((r) => {
				jobs = r.items;
				next = r.next;
			})
			.catch((e) => {
				if (!ctl.signal.aborted) {
					jobs = [];
					next = '';
					error = problemMessage(e);
				}
			})
			.finally(() => {
				if (!ctl.signal.aborted) loaded = true;
			});
		return () => ctl.abort();
	});

	async function more() {
		loadingMore = true;
		try {
			const r = await fetchScopedJobs(scope, next);
			jobs = [...jobs, ...r.items];
			next = r.next;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}
</script>

<div class="space-y-6">
	<div class="flex flex-wrap items-center gap-3">
		<h1 class="text-2xl font-semibold" data-testid="project-title">{scope}</h1>
	</div>

	{#if scope && atLeast(myRole, 'editor')}
		<a
			class="inline-block rounded-md border px-3 py-1 text-sm hover:bg-muted"
			href={resolve('/jobs/new/[...project]', { project: scope })}
			data-testid="new-job">New job in {scope}</a
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
	{/if}

	{#if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else if !error}
		<Table.Root data-testid="jobs-table">
			<Table.Header>
				<Table.Row>
					<Table.Head>Name</Table.Head>
					<Table.Head>Project</Table.Head>
					<Table.Head>Version</Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each jobs as job (job.id)}
					<Table.Row data-testid="job-row">
						<Table.Cell>
							<a
								class="font-medium underline-offset-4 hover:underline"
								href={resolve('/projects/[...path]', { path: jobRest(job.project, job.id) })}
								>{job.name}</a
							>
							{#if job.managed_by !== 'ui'}
								<Badge variant="secondary" class="ml-2" data-testid="managed-{job.name}"
									>managed by {job.managed_by}</Badge
								>
							{/if}
						</Table.Cell>
						<Table.Cell>
							<a
								class="hover:underline"
								href={resolve('/projects/[...path]', { path: job.project })}>{job.project}</a
							>
						</Table.Cell>
						<Table.Cell>{job.current_version}</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row>
						<Table.Cell colspan={3} class="text-muted-foreground">No jobs yet.</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
		{#if next}
			<Button variant="outline" onclick={more} disabled={loadingMore} data-testid="jobs-more"
				>More…</Button
			>
		{/if}
	{/if}
</div>
