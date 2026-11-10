<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import NodeFields from '$lib/components/NodeFields.svelte';
	import TokenGate from '$lib/components/TokenGate.svelte';
	import {
		Unauthorized,
		deleteNode,
		forgetHostKey,
		fetchNode,
		formOf,
		loadToken,
		patchNode,
		problemMessage,
		saveToken,
		type Node,
		type NodeForm
	} from '$lib/nodes';

	const name = $derived(page.params.name ?? '');

	let token = $state('');
	let node = $state<Node | null>(null);
	let loaded = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);
	let editing = $state(false);
	let form = $state<NodeForm | null>(null);
	let force = $state(false);
	let confirmDelete = $state(false);

	const owned = $derived(node !== null && node.source !== 'manual');

	function fail(e: unknown) {
		if (e instanceof Unauthorized) {
			token = '';
			saveToken('');
			node = null;
			error =
				'The admin token was rejected. Enter a valid token (damson admin admin-token create).';
		} else {
			error =
				e instanceof Error && !(e as { detail?: string }).detail ? e.message : problemMessage(e);
		}
	}

	async function load() {
		if (!token) {
			loaded = true;
			return;
		}
		try {
			node = await fetchNode(token, name);
			error = null;
		} catch (e) {
			fail(e);
		} finally {
			loaded = true;
		}
	}

	function useToken(t: string) {
		token = t;
		saveToken(t);
		error = null;
		loaded = false;
		void load();
	}

	function startEdit() {
		if (!node) return;
		form = formOf(node);
		force = false;
		editing = true;
	}

	async function save(e: Event) {
		e.preventDefault();
		if (!node || !form) return;
		busy = true;
		try {
			// if_version: the edit fails with a conflict if someone changed the node meanwhile.
			node = await patchNode(token, node.name, form, node.version, force);
			editing = false;
			force = false; // each forced write is its own decision
			error = null;
		} catch (err) {
			fail(err);
		} finally {
			busy = false;
		}
	}

	async function forget() {
		if (!node) return;
		busy = true;
		try {
			await forgetHostKey(token, node.name);
			await load();
		} catch (err) {
			fail(err);
		} finally {
			busy = false;
		}
	}

	async function remove() {
		if (!node) return;
		busy = true;
		try {
			await deleteNode(token, node.name, node.version, force);
			await goto(resolve('/nodes'));
		} catch (err) {
			fail(err);
			confirmDelete = false;
		} finally {
			busy = false;
		}
	}

	onMount(() => {
		token = loadToken();
		void load();
	});
</script>

<svelte:head>
	<title>{name} - Nodes - Damson</title>
</svelte:head>

<main class="mx-auto max-w-4xl space-y-6 px-4 py-8">
	<p class="text-sm">
		<a class="underline-offset-4 hover:underline" href={resolve('/nodes')}>Nodes</a> / {name}
	</p>

	{#if error}
		<Alert.Root variant="destructive" data-testid="node-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !token}
		<TokenGate onsubmit={useToken} />
	{:else if !loaded}
		<Skeleton class="h-40 w-full" />
	{:else if node}
		<h1 class="text-2xl font-semibold">{node.name}</h1>

		{#if editing && form}
			<form class="space-y-4" data-testid="node-form" onsubmit={save}>
				<NodeFields bind:form editing />
				{#if owned}
					<label class="flex items-center gap-2 text-sm">
						<input type="checkbox" bind:checked={force} data-testid="force" />
						Force: this node belongs to source “{node.source}”; its next sync overwrites the edit
					</label>
				{/if}
				<div class="flex gap-2">
					<Button type="submit" disabled={busy || (owned && !force)} data-testid="save-node"
						>Save</Button
					>
					<Button type="button" variant="outline" onclick={() => (editing = false)}>Cancel</Button>
				</div>
			</form>
		{:else}
			<dl
				class="grid max-w-2xl grid-cols-[10rem_1fr] gap-x-4 gap-y-2 text-sm"
				data-testid="node-details"
			>
				<dt class="text-muted-foreground">Address</dt>
				<dd>{node.address}:{node.port} ({node.transport})</dd>
				<dt class="text-muted-foreground">OS / pool</dt>
				<dd>{node.os || '-'} / {node.pool || '-'}</dd>
				<dt class="text-muted-foreground">Tags</dt>
				<dd>{(node.tags ?? []).join(', ') || '-'}</dd>
				<dt class="text-muted-foreground">Attributes</dt>
				<dd class="font-mono">{JSON.stringify(node.attributes ?? {})}</dd>
				<dt class="text-muted-foreground">Credential</dt>
				<dd class="font-mono">{node.credential || '-'}</dd>
				<dt class="text-muted-foreground">Host key</dt>
				<dd class="font-mono break-all">
					{node.host_key_fingerprint || 'none pinned'} ({node.host_key_policy})
				</dd>
				<dt class="text-muted-foreground">Observed key</dt>
				<dd class="font-mono break-all">
					{node.host_key_observed_fingerprint || 'none'}
					{#if node.host_key_observed}
						<Button
							variant="outline"
							size="sm"
							data-testid="forget-host-key"
							disabled={busy}
							onclick={forget}>Forget</Button
						>
					{/if}
				</dd>
				<dt class="text-muted-foreground">Source</dt>
				<dd>{node.source}{node.external_id ? ` (${node.external_id})` : ''}</dd>
				<dt class="text-muted-foreground">State</dt>
				<dd>{node.enabled ? 'enabled' : 'disabled'}, version {node.version}</dd>
			</dl>
			<div class="flex items-center gap-2">
				<Button data-testid="edit-node" onclick={startEdit}>Edit</Button>
				{#if owned}
					<label class="flex items-center gap-2 text-sm">
						<input type="checkbox" bind:checked={force} data-testid="force-delete" />
						Force (source “{node.source}” may recreate it)
					</label>
				{/if}
				{#if confirmDelete}
					<Button
						variant="destructive"
						disabled={busy || (owned && !force)}
						data-testid="confirm-delete"
						onclick={remove}
					>
						Confirm delete
					</Button>
					<Button variant="outline" onclick={() => (confirmDelete = false)}>No</Button>
				{:else}
					<Button variant="outline" data-testid="delete-node" onclick={() => (confirmDelete = true)}
						>Delete</Button
					>
				{/if}
			</div>
		{/if}
	{/if}
</main>
