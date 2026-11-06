<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Button } from '$lib/components/ui/button';
	import TokenGate from '$lib/components/TokenGate.svelte';
	import {
		Unauthorized,
		fetchRunTargets,
		loadToken,
		problemMessage,
		saveToken,
		type RunTarget
	} from '$lib/nodes';

	/** The nodes a run froze when it started. Needs the admin token, like the inventory. */
	let { runId }: { runId: string } = $props();

	let open = $state(false);
	let token = $state(loadToken());
	let items = $state<RunTarget[]>([]);
	let next = $state<string | undefined>();
	let loaded = $state(false);
	let error = $state<string | null>(null);

	async function load(more = false) {
		if (!token) return;
		try {
			const page = await fetchRunTargets(token, runId, more ? next : undefined);
			items = more ? [...items, ...page.items] : page.items;
			next = page.next;
			error = null;
		} catch (e) {
			if (e instanceof Unauthorized) {
				token = '';
				saveToken('');
				error = 'The admin token was rejected. Enter a valid token.';
			} else {
				error =
					e instanceof Error && !(e as { detail?: string }).detail ? e.message : problemMessage(e);
			}
		} finally {
			loaded = true;
		}
	}

	function toggle() {
		open = !open;
		if (open && !loaded) void load();
	}

	function useToken(t: string) {
		token = t;
		saveToken(t);
		error = null;
		void load();
	}
</script>

<section class="space-y-3" data-testid="run-targets">
	<Button variant="outline" size="sm" data-testid="targets-toggle" onclick={toggle}>
		{open ? 'Hide targets' : 'Show targets'}
	</Button>
	{#if open}
		{#if error}
			<Alert.Root variant="destructive" data-testid="targets-error">
				<Alert.Description>{error}</Alert.Description>
			</Alert.Root>
		{/if}
		{#if !token}
			<TokenGate onsubmit={useToken} />
		{:else if loaded && !items.length && !error}
			<p class="text-sm text-muted-foreground" data-testid="targets-empty">
				This run froze no targets.
			</p>
		{:else if items.length}
			<Table.Root data-testid="targets-table">
				<Table.Header>
					<Table.Row>
						<Table.Head>Name</Table.Head>
						<Table.Head>Address</Table.Head>
						<Table.Head>OS</Table.Head>
						<Table.Head>Pool</Table.Head>
						<Table.Head>Tags</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each items as t (t.node_id)}
						<Table.Row data-testid="target-row">
							<Table.Cell class="font-mono text-xs">{t.name}</Table.Cell>
							<Table.Cell>{t.address}</Table.Cell>
							<Table.Cell>{t.os}</Table.Cell>
							<Table.Cell>{t.pool}</Table.Cell>
							<Table.Cell>{(t.tags ?? []).join(', ')}</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
			{#if next}
				<Button variant="outline" data-testid="targets-more" onclick={() => load(true)}>
					Load more
				</Button>
			{/if}
		{/if}
	{/if}
</section>
