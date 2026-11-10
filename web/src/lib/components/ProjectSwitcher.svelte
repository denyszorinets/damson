<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import * as Select from '$lib/components/ui/select';
	import { fetchProjects, type Project } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';

	/** Jumps to any project the caller can see; the server decides what that is. */
	let { current = '' }: { current?: string } = $props();

	let projects = $state<Project[]>([]);
	let failed = $state(false);

	$effect(() => {
		void fetchProjects().then(
			(list) => (projects = list),
			() => (failed = true)
		);
	});

	function go(path: string) {
		if (path) void goto(resolve('/projects/[...path]', { path: projectRest(path) }));
	}
</script>

<Select.Root type="single" value={current} onValueChange={go}>
	<Select.Trigger class="w-52" aria-label="Switch project" data-testid="project-switcher">
		{current || 'Projects'}
	</Select.Trigger>
	<Select.Content data-testid="project-switcher-list">
		{#each projects as p (p.path)}
			<Select.Item value={p.path} data-testid="switch-{p.path}">{p.path}</Select.Item>
		{:else}
			<p class="px-2 py-1.5 text-sm text-muted-foreground">
				{failed ? 'Could not load projects.' : 'No projects.'}
			</p>
		{/each}
	</Select.Content>
</Select.Root>
