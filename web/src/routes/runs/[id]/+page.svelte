<script lang="ts">
	// The old run URL: it has no project, so it follows the run to its job's project and moves to
	// the project-first URL.
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { fetchJobProject } from '$lib/jobs';
	import { runRest } from '$lib/jobroute';
	import RunView from '$lib/components/RunView.svelte';
	import { fetchRunDetail } from '$lib/runs';

	// Without a project to move to, the run is shown as it is; a run the caller may not read says
	// not found there.
	let unresolved = $state(false);

	$effect(() => {
		const id = page.params.id ?? '';
		const ctl = new AbortController();
		unresolved = false;
		void (async () => {
			try {
				const run = await fetchRunDetail(id, ctl.signal);
				const project = await fetchJobProject(run.job_id, ctl.signal);
				if (ctl.signal.aborted) return;
				if (project) {
					await goto(resolve('/projects/[...path]', { path: runRest(project, id) }), {
						replaceState: true
					});
					return;
				}
			} catch {
				if (ctl.signal.aborted) return;
			}
			unresolved = true;
		})();
		return () => ctl.abort();
	});
</script>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	{#if unresolved}
		{#key page.params.id}<RunView path="" id={page.params.id ?? ''} />{/key}
	{/if}
</main>
