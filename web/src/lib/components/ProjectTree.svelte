<script lang="ts">
	import { resolve } from '$app/paths';
	import { SvelteSet } from 'svelte/reactivity';
	import { Badge } from '$lib/components/ui/badge';
	import { problemMessage } from '$lib/projects';
	import {
		fetchLevel,
		fetchShared,
		loadExpanded,
		saveExpanded,
		type SharedJob,
		type TreeProject
	} from '$lib/tree';
	import TreeNode from './TreeNode.svelte';

	/**
	 * The sidebar: the projects the caller holds a role on, nested by path, with
	 * their jobs, and below them the jobs reached only through a rule of the job.
	 * The server builds every level, so nothing hidden shows here.
	 */
	let { sub, currentJob = '' }: { sub: string; currentJob?: string } = $props();

	let roots = $state<TreeProject[] | null>(null);
	let rootsNext = $state('');
	let loadingMore = $state(false);
	let shared = $state<SharedJob[]>([]);
	let sharedTruncated = $state(false);
	let error = $state('');
	// svelte-ignore state_referenced_locally
	const open = new SvelteSet<string>(loadExpanded(sub));

	function toggle(path: string) {
		if (!open.delete(path)) open.add(path);
		saveExpanded(sub, new Set(open));
	}

	async function moreRoots() {
		loadingMore = true;
		try {
			const level = await fetchLevel('', rootsNext);
			roots = [...(roots ?? []), ...level.projects];
			rootsNext = level.next;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}

	$effect(() => {
		void (async () => {
			try {
				const [level, s] = await Promise.all([fetchLevel(''), fetchShared()]);
				roots = level.projects;
				rootsNext = level.next;
				shared = s.items;
				sharedTruncated = s.truncated;
			} catch (e) {
				error = problemMessage(e);
			}
		})();
	});
</script>

<nav aria-label="Projects" class="text-sm" data-testid="project-tree">
	{#if error}
		<p class="text-destructive" role="alert">{error}</p>
	{:else if roots === null}
		<p class="text-muted-foreground">Loading…</p>
	{:else}
		{#if roots.length}
			<ul>
				{#each roots as project (project.path)}
					<TreeNode {project} {open} ontoggle={toggle} {currentJob} />
				{/each}
			</ul>
			{#if rootsNext}
				<button
					type="button"
					class="px-1 text-xs text-muted-foreground hover:text-foreground"
					onclick={moreRoots}
					disabled={loadingMore}>More…</button
				>
			{/if}
		{:else if !shared.length}
			<p class="text-muted-foreground" data-testid="tree-empty">No projects to show.</p>
		{/if}
		{#if shared.length}
			<h2 class="mt-3 mb-1 px-1 text-xs font-medium text-muted-foreground uppercase">
				Shared with me
			</h2>
			<ul data-testid="tree-shared">
				{#each shared as job (job.id)}
					<li class="flex items-center gap-1">
						<a
							class="min-w-0 flex-1 truncate rounded px-1 py-0.5 hover:bg-muted"
							class:bg-muted={job.id === currentJob}
							href={resolve('/jobs/[id]', { id: job.id })}
							data-testid={`tree-shared-${job.name}`}>{job.name}</a
						>
						<Badge variant="outline">{job.my_role}</Badge>
					</li>
				{/each}
			</ul>
		{/if}
		{#if sharedTruncated}
			<p class="mt-1 px-1 text-xs text-muted-foreground" data-testid="tree-shared-truncated">
				Too many shared jobs to search; some may be missing.
			</p>
		{/if}
	{/if}
</nav>
