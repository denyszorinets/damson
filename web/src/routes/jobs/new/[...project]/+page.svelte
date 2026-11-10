<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import SpecEditor from '$lib/components/SpecEditor.svelte';
	import { createJob, newJobTemplate } from '$lib/specedit';

	// The project is the path after /jobs/new/, from the link that led here; the spec names it.
	let text = $state(newJobTemplate(page.params.project ?? ''));

	async function save(t: string) {
		const id = await createJob(t);
		await goto(resolve('/jobs/[id]', { id }));
	}
</script>

<svelte:head>
	<title>New job - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-baseline gap-3">
		<a
			class="text-sm text-muted-foreground hover:text-foreground"
			href={page.params.project
				? resolve('/projects/[...path]', { path: page.params.project })
				: resolve('/')}>{page.params.project || 'Projects'}</a
		>
		<h1 class="text-2xl font-semibold" data-testid="editor-title">New job</h1>
	</div>
	<SpecEditor bind:text saveLabel="Create job" onsave={save} />
</main>
