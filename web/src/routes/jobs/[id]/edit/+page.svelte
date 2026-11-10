<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import NotFound from '$lib/components/NotFound.svelte';
	import SpecEditor from '$lib/components/SpecEditor.svelte';
	import { isNotFound, problemMessage, type Job } from '$lib/jobs';
	import { atLeast } from '$lib/projects';
	import { createVersion, loadForEdit } from '$lib/specedit';

	const id = $derived(page.params.id ?? '');
	let job = $state<Job | null>(null);
	let text = $state('');
	let missing = $state(false);
	let error = $state<string | null>(null);

	$effect(() => {
		const jobId = id;
		job = null;
		missing = false;
		error = null;
		loadForEdit(jobId)
			.then((r) => {
				if (jobId !== id) return;
				job = r.job;
				text = r.text;
			})
			.catch((e) => {
				if (isNotFound(e)) missing = true;
				else error = problemMessage(e);
			});
	});

	// Why the spec cannot be changed here, or null when it can.
	const reason = $derived.by(() => {
		if (!job) return null;
		if (job.archived) return 'This job is archived: its source no longer defines it.';
		if (job.managed_by !== 'ui')
			return `This job is managed by ${job.managed_by}; change it there and apply.`;
		if (!atLeast(job.my_role, 'editor')) return 'Changing a job needs the editor role.';
		return null;
	});

	async function save(t: string) {
		if (!job) return;
		await createVersion(job.id, t, job.current_version);
		await goto(resolve('/jobs/[id]', { id: job.id }));
	}
</script>

<svelte:head>
	<title>Edit {job?.name ?? 'job'} - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-baseline gap-3">
		<a
			class="text-sm text-muted-foreground hover:text-foreground"
			href={resolve('/jobs/[id]', { id })}>Back to job</a
		>
		<h1 class="text-2xl font-semibold" data-testid="editor-title">
			{job ? `New version of ${job.name}` : 'Edit job'}
		</h1>
		{#if job}<span class="text-sm text-muted-foreground"
				>based on version {job.current_version}</span
			>{/if}
	</div>
	{#if missing}
		<NotFound what="This job" />
	{:else if error}
		<Alert.Root variant="destructive" data-testid="editor-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !job}
		<Skeleton class="h-64 w-full" />
	{:else}
		{#if reason}
			<Alert.Root data-testid="editor-readonly">
				<Alert.Description>{reason} The spec is shown read-only.</Alert.Description>
			</Alert.Root>
		{/if}
		<SpecEditor
			bind:text
			readonly={reason !== null}
			saveLabel="Save new version"
			onsave={save}
			jobId={job.id}
			role={job.my_role}
		/>
	{/if}
</main>
