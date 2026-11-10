<script lang="ts">
	// The old job URL: it has no project, so it asks for the job's project and moves to the
	// project-first URL. A job the caller cannot read shows the same not-found view as a missing one.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import NotFound from '$lib/components/NotFound.svelte';
	import { fetchJobProject } from '$lib/jobs';
	import { jobRest } from '$lib/jobroute';

	let missing = $state(false);

	$effect(() => {
		const id = page.params.id ?? '';
		const ctl = new AbortController();
		missing = false;
		fetchJobProject(id, ctl.signal).then((project) => {
			if (ctl.signal.aborted) return;
			if (project) {
				void goto(resolve('/projects/[...path]', { path: jobRest(project, id) }), {
					replaceState: true
				});
			} else missing = true;
		});
		return () => ctl.abort();
	});
</script>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	{#if missing}
		<NotFound what="This job" />
	{/if}
</main>
