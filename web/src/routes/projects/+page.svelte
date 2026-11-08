<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Select from '$lib/components/ui/select';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import {
		ROLES,
		atLeast,
		canManage,
		createProject,
		deleteProject,
		depth,
		fetchAccess,
		fetchNodeScope,
		fetchProjects,
		isInstanceAdmin,
		leaf,
		problemMessage,
		saveAccess,
		saveNodeScope,
		type AccessRule,
		type Project
	} from '$lib/projects';

	let projects = $state<Project[]>([]);
	let loaded = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);

	const byPath = $derived(new Map(projects.map((p) => [p.path, p])));
	// Where a new project can go: under a project the caller administers, or at
	// the top for an instance admin.
	const parents = $derived(projects.filter((p) => atLeast(p.my_role, 'admin')));
	const canCreateRoot = $derived(isInstanceAdmin());

	let newParent = $state('');
	let newName = $state('');
	let newDescription = $state('');

	// The project whose rules and node scope are being edited.
	let editing = $state<string | null>(null);
	let rules = $state<AccessRule[]>([]);
	let rulesVersion = $state(0);
	let rulesErrors = $state<string[]>([]);
	let nodeScope = $state('');
	let nodeScopeVersion = $state(0);
	let nodeScopeError = $state<string | null>(null);
	let saved = $state<string | null>(null);

	async function load() {
		try {
			projects = await fetchProjects();
			error = null;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loaded = true;
		}
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		saved = null;
		try {
			await fn();
			error = null;
			await load();
		} catch (e) {
			error = problemMessage(e);
		} finally {
			busy = false;
		}
	}

	function create() {
		const name = newName.trim();
		if (!name) return;
		const path = newParent ? `${newParent}/${name}` : name;
		return act(async () => {
			await createProject(path, newDescription.trim());
			newName = newDescription = '';
		});
	}

	function remove(path: string) {
		return act(async () => {
			await deleteProject(path);
			if (editing === path) editing = null;
		});
	}

	/** The messages of a 422 problem: one per invalid field. */
	function fieldMessages(e: unknown): string[] {
		const errs = (e as { errors?: { message?: string; location?: string }[] })?.errors;
		if (!Array.isArray(errs) || errs.length === 0) return [problemMessage(e)];
		return errs.map((f) => `${f.location ?? ''}: ${f.message ?? ''}`);
	}

	async function edit(path: string) {
		editing = path;
		saved = null;
		rulesErrors = [];
		nodeScopeError = null;
		try {
			const [a, n] = await Promise.all([fetchAccess(path), fetchNodeScope(path)]);
			rules = (a.rules ?? []).map((r) => ({ ...r }));
			rulesVersion = a.version;
			nodeScope = n.node_scope;
			nodeScopeVersion = n.version;
		} catch (e) {
			error = problemMessage(e);
			editing = null;
		}
	}

	async function saveRules() {
		if (!editing) return;
		const path = editing;
		busy = true;
		saved = null;
		rulesErrors = [];
		try {
			const out = await saveAccess(path, rules, rulesVersion);
			rules = (out.rules ?? []).map((r) => ({ ...r }));
			rulesVersion = out.version;
			// The project version moved too: take the new node scope version.
			nodeScopeVersion = (await fetchNodeScope(path)).version;
			saved = 'Access rules saved.';
		} catch (e) {
			rulesErrors = fieldMessages(e);
		} finally {
			busy = false;
		}
	}

	async function saveScope() {
		if (!editing) return;
		const path = editing;
		busy = true;
		saved = null;
		nodeScopeError = null;
		try {
			const out = await saveNodeScope(path, nodeScope, nodeScopeVersion);
			nodeScope = out.node_scope;
			nodeScopeVersion = out.version;
			rulesVersion = (await fetchAccess(path)).version;
			saved = 'Node scope saved.';
		} catch (e) {
			nodeScopeError = fieldMessages(e).join('; ');
		} finally {
			busy = false;
		}
	}

	onMount(load);
</script>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-6">
	<h1 class="text-2xl font-semibold">Projects</h1>

	{#if error}
		<Alert.Root variant="destructive" data-testid="project-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !loaded}
		<p class="text-muted-foreground">Loading…</p>
	{:else if projects.length === 0}
		<p class="text-muted-foreground" data-testid="no-projects">
			You do not have access to any project yet.
		</p>
	{:else}
		<ul class="divide-y rounded-lg border" data-testid="project-tree">
			{#each projects as p (p.path)}
				<li
					class="flex flex-wrap items-center gap-3 px-3 py-2"
					style:padding-left="{0.75 + depth(p.path) * 1.5}rem"
					data-testid="project-row-{p.path}"
				>
					<span class="font-medium">{leaf(p.path)}</span>
					<Badge variant="secondary" data-testid="role-{p.path}">{p.my_role ?? 'none'}</Badge>
					{#if p.description}
						<span class="text-sm text-muted-foreground">{p.description}</span>
					{/if}
					<span class="ml-auto flex gap-2">
						{#if atLeast(p.my_role, 'admin')}
							<Button
								size="sm"
								variant="outline"
								data-testid="edit-{p.path}"
								onclick={() => edit(p.path)}>Rules and scope</Button
							>
						{/if}
						{#if canManage(p.path, byPath)}
							<Button
								size="sm"
								variant="outline"
								disabled={busy}
								data-testid="delete-{p.path}"
								onclick={() => remove(p.path)}>Delete</Button
							>
						{/if}
					</span>
				</li>
			{/each}
		</ul>
	{/if}

	{#if loaded && (parents.length > 0 || canCreateRoot)}
		<form
			class="max-w-xl space-y-3 rounded-lg border p-4"
			data-testid="create-project"
			onsubmit={(e) => {
				e.preventDefault();
				create();
			}}
		>
			<h2 class="font-medium">New project</h2>
			<div class="grid gap-2">
				<Label>Parent</Label>
				<Select.Root type="single" value={newParent} onValueChange={(v) => (newParent = v)}>
					<Select.Trigger data-testid="create-parent" class="w-64">
						{newParent || (canCreateRoot ? '(top level)' : 'choose a parent')}
					</Select.Trigger>
					<Select.Content>
						{#if canCreateRoot}
							<Select.Item value="">(top level)</Select.Item>
						{/if}
						{#each parents as p (p.path)}
							<Select.Item value={p.path}>{p.path}</Select.Item>
						{/each}
					</Select.Content>
				</Select.Root>
			</div>
			<div class="grid gap-2">
				<Label for="create-name">Name</Label>
				<Input id="create-name" data-testid="create-name" bind:value={newName} required />
			</div>
			<div class="grid gap-2">
				<Label for="create-description">Description</Label>
				<Input id="create-description" bind:value={newDescription} />
			</div>
			<Button type="submit" disabled={busy} data-testid="create-submit">Create</Button>
		</form>
	{/if}

	{#if editing}
		<section class="space-y-6 rounded-lg border p-4" data-testid="project-editor">
			<h2 class="font-medium">
				<span data-testid="editing-path">{editing}</span>
			</h2>
			{#if saved}
				<Alert.Root data-testid="saved"><Alert.Description>{saved}</Alert.Description></Alert.Root>
			{/if}

			<div class="space-y-3">
				<h3 class="text-sm font-medium">Access rules</h3>
				<p class="text-sm text-muted-foreground">
					A user holds the highest role whose rule is true. Rules apply to this project and the ones
					below it. Example: <code>'billing-ops' in claims.groups</code>
				</p>
				{#each rules as rule, i (i)}
					<div class="flex items-center gap-2">
						<Select.Root
							type="single"
							value={rule.role}
							onValueChange={(v) => (rules[i].role = v as AccessRule['role'])}
						>
							<Select.Trigger data-testid="rule-role-{i}" class="w-32">{rule.role}</Select.Trigger>
							<Select.Content>
								{#each ROLES as r (r)}
									<Select.Item value={r}>{r}</Select.Item>
								{/each}
							</Select.Content>
						</Select.Root>
						<Input
							class="flex-1 font-mono"
							aria-label="Rule {i + 1}"
							data-testid="rule-text-{i}"
							bind:value={rules[i].rule}
						/>
						<Button
							size="sm"
							variant="outline"
							data-testid="rule-remove-{i}"
							onclick={() => rules.splice(i, 1)}>Remove</Button
						>
					</div>
				{/each}
				<div class="flex gap-2">
					<Button
						size="sm"
						variant="outline"
						data-testid="add-rule"
						onclick={() => rules.push({ role: 'viewer', rule: '' })}>Add rule</Button
					>
					<Button size="sm" disabled={busy} data-testid="save-rules" onclick={saveRules}
						>Save rules</Button
					>
				</div>
				{#if rulesErrors.length}
					<Alert.Root variant="destructive" data-testid="rules-error">
						<Alert.Description>
							{#each rulesErrors as m (m)}<div>{m}</div>{/each}
						</Alert.Description>
					</Alert.Root>
				{/if}
			</div>

			<div class="space-y-3">
				<h3 class="text-sm font-medium">Node scope</h3>
				<p class="text-sm text-muted-foreground">
					Which nodes jobs in this project may use, as an expression over <code>node</code>; empty
					allows every node. Example: <code>'prod' in node.tags</code>
				</p>
				<Input
					class="font-mono"
					aria-label="Node scope"
					data-testid="node-scope"
					bind:value={nodeScope}
				/>
				<Button size="sm" disabled={busy} data-testid="save-node-scope" onclick={saveScope}
					>Save node scope</Button
				>
				{#if nodeScopeError}
					<Alert.Root variant="destructive" data-testid="node-scope-error">
						<Alert.Description>{nodeScopeError}</Alert.Description>
					</Alert.Root>
				{/if}
			</div>
		</section>
	{/if}
</main>
