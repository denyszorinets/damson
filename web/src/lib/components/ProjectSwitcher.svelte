<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import * as Select from '$lib/components/ui/select';
	import { isTyping } from '$lib/keys';
	import { fetchProjects, type Project } from '$lib/projects';
	import { projectRest } from '$lib/projectroute';

	/** Jumps to any project the caller can see; the server decides what that is. "/" opens it. */
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

	// "/" opens the switcher from anywhere the user is not typing: it focuses the trigger and
	// presses the key that opens it, so the list takes focus and the arrow keys work.
	function onkeydown(e: KeyboardEvent) {
		if (e.key !== '/' || e.altKey || e.ctrlKey || e.metaKey || e.defaultPrevented) return;
		// Not while typing, nor inside an open list or dialog: a "/" there belongs to it (project
		// paths contain slashes, so typeahead needs them).
		if (
			isTyping(e.target) ||
			(e.target as Element | null)?.closest?.('[role=listbox], [role=dialog]')
		)
			return;
		const trigger = document.querySelector<HTMLElement>('[data-testid="project-switcher"]');
		if (!trigger) return;
		e.preventDefault();
		trigger.focus();
		trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
	}
</script>

<svelte:window {onkeydown} />

<Select.Root type="single" value={current} onValueChange={go}>
	<Select.Trigger
		class="w-36 sm:w-52"
		aria-label="Switch project"
		aria-keyshortcuts="/"
		title="Switch project (press /)"
		data-testid="project-switcher"
	>
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
