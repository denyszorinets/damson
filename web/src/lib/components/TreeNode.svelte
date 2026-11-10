<script lang="ts">
	import { resolve } from '$app/paths';
	import type { SvelteSet } from 'svelte/reactivity';
	import { Badge } from '$lib/components/ui/badge';
	import { problemMessage } from '$lib/projects';
	import { fetchLevel, type TreeLevel, type TreeProject } from '$lib/tree';
	import TreeNode from './TreeNode.svelte';

	/**
	 * A project of the sidebar tree. Its child projects and jobs load when it is
	 * first opened, one page at a time; what is open is kept by the parent.
	 */
	let {
		project,
		open,
		ontoggle,
		currentJob = ''
	}: {
		project: TreeProject;
		open: SvelteSet<string>;
		ontoggle: (path: string) => void;
		currentJob?: string;
	} = $props();

	let level = $state<TreeLevel | null>(null);
	let loading = $state(false);
	let attempted = false; // not reactive: a failed load must not re-run the effect
	let error = $state('');
	const isOpen = $derived(open.has(project.path));

	$effect(() => {
		if (isOpen && !attempted) void load();
	});

	/**
	 * Loads the first page, or the next one. The server pages projects and jobs
	 * apart and always answers both lists, so a page continues only the list whose
	 * cursor is set: the other is dropped, never appended twice.
	 */
	async function load(more = false) {
		attempted = true;
		loading = true;
		try {
			const cur = more ? level : null;
			const next = await fetchLevel(project.path, cur?.next ?? '', cur?.jobsNext ?? '');
			level =
				cur === null
					? next
					: {
							projects: cur.next ? [...cur.projects, ...next.projects] : cur.projects,
							jobs: cur.jobsNext ? [...cur.jobs, ...next.jobs] : cur.jobs,
							next: cur.next ? next.next : '',
							jobsNext: cur.jobsNext ? next.jobsNext : ''
						};
			error = '';
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loading = false;
		}
	}
</script>

<li data-testid={`tree-project-${project.path}`}>
	<div class="flex items-center gap-1">
		<button
			type="button"
			class="flex min-w-0 flex-1 items-center gap-1 rounded px-1 py-0.5 text-left text-sm hover:bg-muted"
			aria-expanded={isOpen}
			onclick={() => ontoggle(project.path)}
			data-testid={`tree-toggle-${project.path}`}
		>
			<span aria-hidden="true" class="w-3 text-muted-foreground">{isOpen ? '▾' : '▸'}</span>
			<span class="truncate" title={project.path}>{project.label}</span>
		</button>
		<Badge variant="outline" data-testid={`tree-role-${project.path}`}>{project.my_role}</Badge>
	</div>
	{#if isOpen}
		<ul class="ml-3 border-l pl-2">
			{#if error}
				<li class="text-xs text-destructive" role="alert">{error}</li>
			{/if}
			{#if level}
				{#each level.projects as child (child.path)}
					<TreeNode project={child} {open} {ontoggle} {currentJob} />
				{/each}
				{#each level.jobs as job (job.id)}
					<li>
						<a
							class="block truncate rounded px-1 py-0.5 text-sm hover:bg-muted"
							class:bg-muted={job.id === currentJob}
							aria-current={job.id === currentJob ? 'page' : undefined}
							href={resolve('/jobs/[id]', { id: job.id })}
							data-testid={`tree-job-${project.path}/${job.name}`}>{job.name}</a
						>
					</li>
				{/each}
				{#if level.next || level.jobsNext}
					<li>
						<button
							type="button"
							class="px-1 text-xs text-muted-foreground hover:text-foreground"
							onclick={() => load(true)}
							disabled={loading}>More…</button
						>
					</li>
				{/if}
				{#if !level.projects.length && !level.jobs.length && !loading}
					<li class="px-1 text-xs text-muted-foreground">Empty</li>
				{/if}
			{:else if loading}
				<li class="px-1 text-xs text-muted-foreground">Loading…</li>
			{/if}
		</ul>
	{/if}
</li>
