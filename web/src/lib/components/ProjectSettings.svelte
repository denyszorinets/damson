<script lang="ts">
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Label } from '$lib/components/ui/label';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Textarea } from '$lib/components/ui/textarea';
	import {
		canManage,
		deleteProject,
		fetchProject,
		fetchProjects,
		parentOf,
		problemMessage,
		updateProjectDescription,
		type Project
	} from '$lib/projects';
	import { projectRest } from '$lib/projectroute';

	/** The project's own settings: its description, and deleting it when it is empty. */
	let { path }: { path: string } = $props();

	let project = $state<Project | null>(null);
	let canDelete = $state(false);
	let description = $state('');
	let loaded = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);
	let saved = $state(false);
	let confirming = $state(false);

	// A project that apply manages is read-only here: its source changes it.
	const managed = $derived((project?.managed_by ?? 'ui') !== 'ui');

	$effect(() => {
		const current = path;
		loaded = false;
		void (async () => {
			try {
				const [p, all] = await Promise.all([fetchProject(current), fetchProjects()]);
				if (current !== path) return;
				project = p;
				description = p?.description ?? '';
				canDelete = canManage(current, new Map(all.map((x) => [x.path, x])));
			} catch (e) {
				error = problemMessage(e);
			} finally {
				if (current === path) loaded = true;
			}
		})();
	});

	async function save() {
		if (!project) return;
		busy = true;
		saved = false;
		error = null;
		try {
			project = await updateProjectDescription(path, description, project.version);
			description = project.description ?? '';
			saved = true;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			busy = false;
		}
	}

	async function remove() {
		busy = true;
		error = null;
		try {
			await deleteProject(path);
			const parent = parentOf(path);
			await goto(
				parent ? resolve('/projects/[...path]', { path: projectRest(parent) }) : resolve('/')
			);
		} catch (e) {
			error = problemMessage(e);
			confirming = false;
		} finally {
			busy = false;
		}
	}
</script>

<div class="space-y-6" data-testid="settings">
	<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>

	{#if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else if !project}
		<Alert.Root variant="destructive" data-testid="settings-error">
			<Alert.Description>Not found, or you may not have access.</Alert.Description>
		</Alert.Root>
	{:else}
		{#if error}
			<Alert.Root variant="destructive" data-testid="settings-error">
				<Alert.Description>{error}</Alert.Description>
			</Alert.Root>
		{/if}

		<section class="space-y-3" aria-label="Description">
			<h2 class="text-lg font-medium">Description</h2>
			{#if managed}
				<Badge variant="secondary" data-testid="settings-managed"
					>managed by {project.managed_by}; change it there</Badge
				>
			{/if}
			<Label for="project-description">Description</Label>
			<Textarea
				id="project-description"
				bind:value={description}
				disabled={managed}
				class="max-w-xl"
				data-testid="settings-description"
			/>
			<div class="flex items-center gap-3">
				<Button onclick={save} disabled={busy || managed} data-testid="settings-save">Save</Button>
				{#if saved}<span class="text-sm text-muted-foreground" data-testid="settings-saved"
						>Saved.</span
					>{/if}
			</div>
		</section>

		<section class="space-y-3" aria-label="Delete">
			<h2 class="text-lg font-medium">Delete this project</h2>
			<p class="text-sm text-muted-foreground">
				A project can be deleted only when it has no child projects and no jobs.
			</p>
			{#if !canDelete}
				<p class="text-sm text-muted-foreground" data-testid="settings-delete-needs">
					Deleting needs admin on the parent project{parentOf(path)
						? ''
						: ', or an instance admin'}.
				</p>
			{:else if managed}
				<p class="text-sm text-muted-foreground">Managed projects are removed by their source.</p>
			{:else if !confirming}
				<Button
					variant="destructive"
					onclick={() => (confirming = true)}
					disabled={busy}
					data-testid="settings-delete">Delete project</Button
				>
			{:else}
				<div class="flex items-center gap-3">
					<span class="text-sm">Delete {path}?</span>
					<Button
						variant="destructive"
						onclick={remove}
						disabled={busy}
						data-testid="settings-delete-confirm">Yes, delete</Button
					>
					<Button variant="outline" onclick={() => (confirming = false)}>Cancel</Button>
				</div>
			{/if}
		</section>
	{/if}
</div>
