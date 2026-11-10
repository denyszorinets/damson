<script lang="ts">
	import { SESSION_TOKEN } from '$lib/session';
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import * as Select from '$lib/components/ui/select';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Textarea } from '$lib/components/ui/textarea';
	import { Label } from '$lib/components/ui/label';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		Unauthorized,
		deleteSecret,
		fetchSecretTypes,
		fetchSecrets,
		loadToken,
		problemMessage,
		putSecret,
		rowsFor,
		saveToken,
		type FieldRow,
		type SecretMeta,
		type SecretType
	} from '$lib/secrets';

	let token = $state('');
	let tokenInput = $state('');
	let secrets = $state<SecretMeta[]>([]);
	let types = $state<SecretType[]>([]);
	let loaded = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);

	// The form. Values are never pre-filled and are cleared after every submit.
	let formOpen = $state(false);
	let editing = $state<SecretMeta | null>(null); // set for "New version"
	let scope = $state('');
	let name = $state('');
	let typeName = $state('generic');
	let rows = $state<FieldRow[]>([]);
	let confirmRef = $state<string | null>(null);

	const currentType = $derived(types.find((t) => t.name === typeName));

	function clearValues() {
		for (const r of rows) r.value = '';
	}

	async function load() {
		if (!token) {
			loaded = true;
			return;
		}
		try {
			secrets = await fetchSecrets(token);
			error = null;
		} catch (e) {
			fail(e);
		} finally {
			loaded = true;
		}
	}

	function fail(e: unknown) {
		if (e instanceof Unauthorized) {
			forget();
			error =
				'The admin token was rejected. Enter a valid token (damson admin admin-token create).';
		} else {
			error = problemMessage(e);
		}
	}

	function forget() {
		token = '';
		saveToken('');
		secrets = [];
		closeForm();
	}

	function useToken() {
		token = tokenInput.trim();
		tokenInput = '';
		if (!token) return;
		saveToken(token);
		error = null;
		loaded = false;
		void load();
	}

	function openCreate() {
		editing = null;
		scope = '';
		name = '';
		typeName = 'generic';
		rows = rowsFor(types.find((t) => t.name === 'generic'));
		formOpen = true;
	}

	function openVersion(s: SecretMeta) {
		editing = s;
		scope = s.scope;
		name = s.name;
		typeName = s.type;
		rows = rowsFor(types.find((t) => t.name === s.type));
		if (s.type === 'generic') {
			rows = (s.fields ?? []).map((f) => ({
				name: f,
				value: '',
				required: false,
				multiline: false
			}));
		}
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
				scope.trim(),
				name.trim(),
				typeName,
				rows,
				editing ? editing.current_version : 0
			);
			error = null;
			closeForm();
			await load();
		} catch (err) {
			fail(err);
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
			fail(err);
		} finally {
			busy = false;
		}
	}

	onMount(async () => {
		token = loadToken();
		try {
			types = await fetchSecretTypes();
		} catch (e) {
			error = problemMessage(e);
		}
		await load();
	});
</script>

<svelte:head>
	<title>Secrets - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Secrets</h1>
		{#if token}
			<div class="flex gap-2">
				<Button data-testid="new-secret" onclick={openCreate} disabled={formOpen}>New secret</Button
				>
				{#if token !== SESSION_TOKEN}
					<Button variant="outline" data-testid="forget-token" onclick={forget}>Forget token</Button
					>
				{/if}
			</div>
		{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="secrets-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !token}
		<form
			class="max-w-md space-y-3"
			data-testid="token-form"
			onsubmit={(e) => {
				e.preventDefault();
				useToken();
			}}
		>
			<Label for="admin-token">Admin token</Label>
			<Input
				id="admin-token"
				type="password"
				autocomplete="off"
				placeholder="dma_..."
				bind:value={tokenInput}
			/>
			<p class="text-sm text-muted-foreground">
				Create one with <code>damson admin admin-token create</code>. It is kept in this tab only.
			</p>
			<Button type="submit">Use token</Button>
		</form>
	{:else if formOpen}
		<form class="max-w-xl space-y-4" data-testid="secret-form" onsubmit={submit} autocomplete="off">
			<h2 class="text-lg font-medium">
				{editing ? `New version of ${editing.ref}` : 'New secret'}
			</h2>
			<div class="grid gap-2">
				<Label for="secret-scope">Scope</Label>
				<Input
					id="secret-scope"
					bind:value={scope}
					disabled={editing !== null}
					placeholder="project or project/sub, or _inventory"
					required
				/>
			</div>
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
						<Textarea
							id="field-{i}"
							data-testid="field-{row.name}"
							bind:value={row.value}
							autocomplete="off"
							rows={6}
						/>
					{:else}
						<Input
							id="field-{i}"
							data-testid="field-{row.name}"
							type="password"
							bind:value={row.value}
							autocomplete="off"
							placeholder={editing ? 'new value' : ''}
						/>
					{/if}
				</div>
			{/each}
			{#if !currentType?.fields?.length}
				<Button
					type="button"
					variant="outline"
					onclick={() => rows.push({ name: '', value: '', required: false, multiline: false })}
				>
					Add field
				</Button>
			{/if}
			<div class="flex gap-2">
				<Button type="submit" disabled={busy} data-testid="save-secret">Save</Button>
				<Button type="button" variant="outline" onclick={closeForm}>Cancel</Button>
			</div>
		</form>
	{/if}

	{#if token}
		{#if !loaded}
			<Skeleton class="h-24 w-full" />
		{:else}
			<Table.Root data-testid="secrets-table">
				<Table.Header>
					<Table.Row>
						<Table.Head>Reference</Table.Head>
						<Table.Head>Type</Table.Head>
						<Table.Head>Fields</Table.Head>
						<Table.Head>Version</Table.Head>
						<Table.Head></Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each secrets as s (s.ref)}
						<Table.Row data-testid="secret-row">
							<Table.Cell class="font-mono text-sm">{s.ref}</Table.Cell>
							<Table.Cell>{s.type}</Table.Cell>
							<Table.Cell>{(s.fields ?? []).join(', ')}</Table.Cell>
							<Table.Cell>{s.current_version}</Table.Cell>
							<Table.Cell class="space-x-2 text-right">
								<Button size="sm" variant="outline" onclick={() => openVersion(s)}
									>New version</Button
								>
								{#if confirmRef === s.ref}
									<Button size="sm" variant="destructive" disabled={busy} onclick={() => remove(s)}>
										Confirm delete
									</Button>
									<Button size="sm" variant="outline" onclick={() => (confirmRef = null)}>No</Button
									>
								{:else}
									<Button size="sm" variant="outline" onclick={() => (confirmRef = s.ref)}
										>Delete</Button
									>
								{/if}
							</Table.Cell>
						</Table.Row>
					{:else}
						<Table.Row>
							<Table.Cell colspan={5} class="text-muted-foreground">No secrets yet.</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		{/if}
	{/if}
</main>
