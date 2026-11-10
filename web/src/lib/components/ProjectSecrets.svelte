<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Select from '$lib/components/ui/select';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { Textarea } from '$lib/components/ui/textarea';
	import { SESSION_TOKEN } from '$lib/session';
	import {
		deleteSecret,
		fetchProjectSecrets,
		fetchSecretTypes,
		problemMessage,
		putSecret,
		rowsFor,
		type FieldRow,
		type SecretMeta,
		type SecretType
	} from '$lib/secrets';

	/**
	 * The secrets a job of this project may use: its own, which a project admin
	 * manages, and its ancestors', labelled with where they come from and read-only
	 * here. Values are never shown: the page holds only names and a form whose
	 * values are cleared after every submit.
	 */
	let { path, canWrite }: { path: string; canWrite: boolean } = $props();

	// A logged-in user authenticates with the session cookie.
	const token = SESSION_TOKEN;

	let secrets = $state<SecretMeta[]>([]);
	let types = $state<SecretType[]>([]);
	let loaded = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);

	let formOpen = $state(false);
	let editing = $state<SecretMeta | null>(null);
	let name = $state('');
	let typeName = $state('generic');
	let rows = $state<FieldRow[]>([]);
	let confirmRef = $state<string | null>(null);

	const currentType = $derived(types.find((t) => t.name === typeName));
	// Own scope first, then the ancestors from the nearest.
	const sorted = $derived(
		[...secrets].sort(
			(a, b) =>
				Number(b.scope === path) - Number(a.scope === path) ||
				b.scope.length - a.scope.length ||
				a.name.localeCompare(b.name)
		)
	);

	function clearValues() {
		for (const r of rows) r.value = '';
	}

	async function load() {
		try {
			secrets = await fetchProjectSecrets(token, path);
			error = null;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loaded = true;
		}
	}

	function openCreate() {
		editing = null;
		name = '';
		typeName = 'generic';
		rows = rowsFor(types.find((t) => t.name === 'generic'));
		formOpen = true;
	}

	function openVersion(s: SecretMeta) {
		editing = s;
		name = s.name;
		typeName = s.type;
		rows =
			s.type === 'generic'
				? (s.fields ?? []).map((f) => ({ name: f, value: '', required: false, multiline: false }))
				: rowsFor(types.find((t) => t.name === s.type));
		formOpen = true;
	}

	function closeForm() {
		clearValues();
		rows = [];
		formOpen = false;
		editing = null;
	}

	function pickType(value: string) {
		typeName = value;
		rows = rowsFor(types.find((t) => t.name === value));
	}

	async function submit(e: Event) {
		e.preventDefault();
		busy = true;
		try {
			await putSecret(
				token,
				path,
				name.trim(),
				typeName,
				rows,
				editing ? editing.current_version : 0
			);
			error = null;
			closeForm();
			await load();
		} catch (err) {
			error = problemMessage(err);
		} finally {
			clearValues(); // success or failure: the values do not stay in the page
			busy = false;
		}
	}

	async function remove(s: SecretMeta) {
		busy = true;
		try {
			await deleteSecret(token, s.scope, s.name);
			confirmRef = null;
			await load();
		} catch (err) {
			error = problemMessage(err);
		} finally {
			busy = false;
		}
	}

	onMount(async () => {
		try {
			types = await fetchSecretTypes();
		} catch (e) {
			error = problemMessage(e);
		}
		await load();
	});
</script>

<div class="space-y-6" data-testid="project-secrets">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>
		{#if canWrite}
			<Button data-testid="new-secret" onclick={openCreate} disabled={formOpen}>New secret</Button>
		{/if}
	</div>
	<p class="text-sm text-muted-foreground">
		Secrets a job of this project may use: this project's own and those of the projects above it.
		Values are never shown.
	</p>

	{#if error}
		<Alert.Root variant="destructive" data-testid="secrets-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if formOpen}
		<form class="max-w-xl space-y-4" data-testid="secret-form" onsubmit={submit} autocomplete="off">
			<h2 class="text-lg font-medium">
				{editing ? `New version of ${editing.name}` : 'New secret'} in {path}
			</h2>
			<div class="grid gap-2">
				<Label for="secret-name">Name</Label>
				<Input id="secret-name" bind:value={name} disabled={editing !== null} required />
			</div>
			<div class="grid gap-2">
				<Label>Type</Label>
				<Select.Root
					type="single"
					value={typeName}
					onValueChange={pickType}
					disabled={editing !== null}
				>
					<Select.Trigger data-testid="secret-type" class="w-64">{typeName}</Select.Trigger>
					<Select.Content>
						{#each types as t (t.name)}
							<Select.Item value={t.name}>{t.name}</Select.Item>
						{/each}
					</Select.Content>
				</Select.Root>
			</div>
			{#each rows as row, i (i)}
				<div class="grid gap-2">
					{#if currentType?.fields?.length}
						<Label for="field-{i}">{row.name}{row.required ? ' *' : ''}</Label>
					{:else}
						<Input bind:value={row.name} placeholder="field name" aria-label="Field name" />
					{/if}
					{#if row.multiline}
						<Textarea id="field-{i}" bind:value={row.value} data-testid="field-{row.name}" />
					{:else}
						<Input
							id="field-{i}"
							type="password"
							autocomplete="new-password"
							bind:value={row.value}
							data-testid="field-{row.name}"
						/>
					{/if}
				</div>
			{/each}
			<div class="flex gap-2">
				<Button type="submit" disabled={busy} data-testid="save-secret">Save</Button>
				<Button type="button" variant="outline" onclick={closeForm}>Cancel</Button>
			</div>
		</form>
	{/if}

	{#if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else if !error}
		<Table.Root data-testid="secrets-table">
			<Table.Header>
				<Table.Row>
					<Table.Head>Reference</Table.Head>
					<Table.Head>Origin</Table.Head>
					<Table.Head>Type</Table.Head>
					<Table.Head>Fields</Table.Head>
					<Table.Head>Version</Table.Head>
					<Table.Head></Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each sorted as s (s.ref)}
					<Table.Row data-testid="secret-row" data-scope={s.scope}>
						<Table.Cell class="font-mono text-sm">{s.ref}</Table.Cell>
						<Table.Cell>
							{#if s.scope === path}
								<Badge variant="secondary" data-testid="origin-own">this project</Badge>
							{:else}
								<Badge variant="outline" data-testid="origin-inherited"
									>inherited from {s.scope}</Badge
								>
							{/if}
						</Table.Cell>
						<Table.Cell>{s.type}</Table.Cell>
						<Table.Cell>{(s.fields ?? []).join(', ')}</Table.Cell>
						<Table.Cell>{s.current_version}</Table.Cell>
						<Table.Cell class="space-x-2 text-right">
							{#if canWrite && s.scope === path}
								<Button
									variant="outline"
									size="sm"
									disabled={busy || formOpen}
									onclick={() => openVersion(s)}
									data-testid="new-version">New version</Button
								>
								{#if confirmRef === s.ref}
									<Button
										variant="destructive"
										size="sm"
										disabled={busy}
										onclick={() => remove(s)}
										data-testid="confirm-delete">Confirm delete</Button
									>
								{:else}
									<Button
										variant="outline"
										size="sm"
										disabled={busy}
										onclick={() => (confirmRef = s.ref)}
										data-testid="delete-secret">Delete</Button
									>
								{/if}
							{/if}
						</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row>
						<Table.Cell colspan={6} class="text-muted-foreground">No secrets yet.</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
	{/if}
</div>
