<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import {
		Unauthorized,
		detailsText,
		fetchAudit,
		loadToken,
		problemMessage,
		saveToken,
		type AuditEvent
	} from '$lib/audit';

	let token = $state('');
	let tokenInput = $state('');
	let events = $state<AuditEvent[]>([]);
	let next = $state('');
	let loaded = $state(false);
	let loadingMore = $state(false);
	let error = $state<string | null>(null);
	let targetType = $state('');
	let target = $state('');

	async function load(more = false) {
		if (!token) {
			loaded = true;
			return;
		}
		if (more) loadingMore = true;
		try {
			const page = await fetchAudit(token, { targetType, target }, more ? next : '');
			events = more ? [...events, ...page.items] : page.items;
			next = page.next;
			error = null;
		} catch (e) {
			fail(e);
		} finally {
			loaded = true;
			loadingMore = false;
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
		events = [];
		next = '';
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

	function applyFilter(e: Event) {
		e.preventDefault();
		loaded = false;
		void load();
	}

	onMount(() => {
		token = loadToken();
		void load();
	});
</script>

<svelte:head><title>Audit - Damson</title></svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Audit</h1>
		{#if token}
			<Button variant="outline" data-testid="forget-token" onclick={forget}>Forget token</Button>
		{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="audit-error">
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
	{:else}
		<form class="flex flex-wrap items-end gap-3" data-testid="audit-filter" onsubmit={applyFilter}>
			<div class="grid gap-2">
				<Label for="audit-target-type">Target type</Label>
				<Input
					id="audit-target-type"
					bind:value={targetType}
					placeholder="secret, execution or instance"
				/>
			</div>
			<div class="grid gap-2">
				<Label for="audit-target">Target</Label>
				<Input id="audit-target" bind:value={target} placeholder="scope/name or execution id" />
			</div>
			<Button type="submit">Filter</Button>
		</form>

		{#if !loaded}
			<Skeleton class="h-24 w-full" />
		{:else}
			<Table.Root data-testid="audit-table">
				<Table.Header>
					<Table.Row>
						<Table.Head>Time (UTC)</Table.Head>
						<Table.Head>Actor</Table.Head>
						<Table.Head>Action</Table.Head>
						<Table.Head>Target</Table.Head>
						<Table.Head>Details</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each events as ev (ev.id)}
						<Table.Row data-testid="audit-row">
							<Table.Cell class="font-mono text-xs whitespace-nowrap">
								{ev.at.replace('T', ' ').replace(/\.\d+Z$|Z$/, '')}
							</Table.Cell>
							<Table.Cell class="font-mono text-sm">{ev.actor}</Table.Cell>
							<Table.Cell>{ev.action}</Table.Cell>
							<Table.Cell class="font-mono text-sm">{ev.target_type}: {ev.target}</Table.Cell>
							<Table.Cell
								class="max-w-xs truncate font-mono text-xs"
								title={detailsText(ev.details)}
							>
								{detailsText(ev.details)}
							</Table.Cell>
						</Table.Row>
					{:else}
						<Table.Row>
							<Table.Cell colspan={5} class="text-muted-foreground">No audit events.</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
			{#if next}
				<Button
					variant="outline"
					data-testid="audit-more"
					disabled={loadingMore}
					onclick={() => load(true)}>Load more</Button
				>
			{/if}
		{/if}
	{/if}
</main>
