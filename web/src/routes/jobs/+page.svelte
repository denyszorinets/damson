<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { poll } from '$lib/polling';
	import { fetchJobs, problemMessage, type Job } from '$lib/jobs';

	const REFRESH_MS = 10000;

	let jobs = $state<Job[]>([]);
	let loaded = $state(false);
	let error = $state<string | null>(null);

	async function refresh(signal: AbortSignal) {
		try {
			const list = await fetchJobs(signal);
			if (signal.aborted) return;
			jobs = list;
			error = null;
		} catch (e) {
			if (signal.aborted) return;
			error = problemMessage(e);
		} finally {
			if (!signal.aborted) loaded = true;
		}
	}

	onMount(() => poll(refresh, REFRESH_MS));
</script>

<svelte:head>
	<title>Jobs - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<h1 class="text-2xl font-semibold">Jobs</h1>

	{#if error}
		<Alert.Root variant="destructive" data-testid="jobs-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else}
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
								href={resolve('/jobs/[id]', { id: job.id })}>{job.name}</a
							>
						</Table.Cell>
						<Table.Cell>{job.project}</Table.Cell>
						<Table.Cell>{job.current_version}</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row>
						<Table.Cell colspan={3} class="text-muted-foreground">No jobs yet.</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
	{/if}
</main>
