<script lang="ts">
	import { SESSION_TOKEN } from '$lib/session';
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import NodeFields from '$lib/components/NodeFields.svelte';
	import TokenGate from '$lib/components/TokenGate.svelte';
	import {
		Unauthorized,
		createNode,
		emptyForm,
		fetchNodes,
		loadToken,
		problemMessage,
		saveToken,
		type Node,
		type NodeForm
	} from '$lib/nodes';

	let token = $state('');
	let nodes = $state<Node[]>([]);
	let next = $state<string | undefined>();
	let loaded = $state(false);
	let error = $state<string | null>(null);
	let busy = $state(false);
	let source = $state('');
	let enabled = $state('');
	let selector = $state('');
	let timer: ReturnType<typeof setTimeout> | undefined;
	let creating = $state(false);
	let form = $state<NodeForm>(emptyForm());

	function fail(e: unknown) {
		if (e instanceof Unauthorized) {
			forget();
			error =
				'The admin token was rejected. Enter a valid token (damson admin admin-token create).';
		} else {
			error =
				e instanceof Error && !(e as { detail?: string }).detail ? e.message : problemMessage(e);
		}
	}

	function forget() {
		token = '';
		saveToken('');
		nodes = [];
		creating = false;
	}

	async function load(more = false) {
		if (!token) {
			loaded = true;
			return;
		}
		try {
			const page = await fetchNodes(token, {
				source,
				enabled,
				selector,
				cursor: more ? next : undefined
			});
			nodes = more ? [...nodes, ...page.items] : page.items;
			next = page.next;
			error = null;
		} catch (e) {
			fail(e);
		} finally {
			loaded = true;
		}
	}

	/** Reloads shortly after the selector stops changing. */
	function selectorChanged() {
		clearTimeout(timer);
		timer = setTimeout(() => void load(), 400);
	}

	function useToken(t: string) {
		token = t;
		saveToken(t);
		error = null;
		loaded = false;
		void load();
	}

	async function create(e: Event) {
		e.preventDefault();
		busy = true;
		try {
			await createNode(token, form);
			creating = false;
			error = null;
			await load();
		} catch (err) {
			fail(err);
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
	<title>Nodes - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Nodes</h1>
		{#if token}
			<div class="flex gap-2">
				<Button
					data-testid="new-node"
					disabled={creating}
					onclick={() => {
						form = emptyForm();
						creating = true;
					}}>New node</Button
				>
				{#if token !== SESSION_TOKEN}
					<Button variant="outline" onclick={forget}>Forget token</Button>
				{/if}
			</div>
		{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="nodes-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !token}
		<TokenGate onsubmit={useToken} />
	{:else}
		{#if creating}
			<form class="space-y-4" data-testid="node-form" onsubmit={create}>
				<h2 class="text-lg font-medium">New node</h2>
				<NodeFields bind:form />
				<div class="flex gap-2">
					<Button type="submit" disabled={busy} data-testid="save-node">Save</Button>
					<Button type="button" variant="outline" onclick={() => (creating = false)}>Cancel</Button>
				</div>
			</form>
		{/if}

		<form
			class="flex items-end gap-3"
			onsubmit={(e) => {
				e.preventDefault();
				void load();
			}}
		>
			<Input
				class="w-48"
				placeholder="source (for example manual)"
				aria-label="Source"
				bind:value={source}
			/>
			<Input
				class="w-40"
				placeholder="enabled: true/false"
				aria-label="Enabled"
				bind:value={enabled}
			/>
			<Input
				data-testid="filter-selector"
				class="w-72"
				aria-label="Selector"
				placeholder="selector, e.g. node.os == 'linux'"
				bind:value={selector}
				oninput={selectorChanged}
			/>
			<Button type="submit" variant="outline">Filter</Button>
		</form>

		{#if !loaded}
			<Skeleton class="h-24 w-full" />
		{:else}
			<Table.Root data-testid="nodes-table">
				<Table.Header>
					<Table.Row>
						<Table.Head>Name</Table.Head>
						<Table.Head>Address</Table.Head>
						<Table.Head>Transport</Table.Head>
						<Table.Head>OS</Table.Head>
						<Table.Head>Pool</Table.Head>
						<Table.Head>Tags</Table.Head>
						<Table.Head>Source</Table.Head>
						<Table.Head>State</Table.Head>
						<Table.Head>Host key</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each nodes as n (n.id)}
						<Table.Row data-testid="node-row">
							<Table.Cell>
								<a
									class="font-medium underline-offset-4 hover:underline"
									href={resolve('/nodes/[name]', { name: n.name })}>{n.name}</a
								>
							</Table.Cell>
							<Table.Cell>{n.address}:{n.port}</Table.Cell>
							<Table.Cell>{n.transport}</Table.Cell>
							<Table.Cell>{n.os}</Table.Cell>
							<Table.Cell>{n.pool}</Table.Cell>
							<Table.Cell class="space-x-1">
								{#each n.tags ?? [] as t (t)}<Badge variant="secondary">{t}</Badge>{/each}
							</Table.Cell>
							<Table.Cell>{n.source}</Table.Cell>
							<Table.Cell>{n.enabled ? 'enabled' : 'disabled'}</Table.Cell>
							<Table.Cell>
								{n.host_key ? 'pinned' : n.host_key_observed ? 'observed' : 'none'} ({n.host_key_policy})
							</Table.Cell>
						</Table.Row>
					{:else}
						<Table.Row>
							<Table.Cell colspan={9} class="text-muted-foreground">No nodes.</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
			{#if next}
				<Button variant="outline" data-testid="load-more" onclick={() => load(true)}
					>Load more</Button
				>
			{/if}
		{/if}
	{/if}
</main>
