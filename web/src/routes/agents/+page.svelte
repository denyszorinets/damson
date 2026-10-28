<script lang="ts">
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Card from '$lib/components/ui/card';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { poll } from '$lib/polling';
	import {
		fetchAgents,
		fetchPools,
		MAX_ROWS,
		formatBytes,
		problemMessage,
		type Agent,
		type Pool
	} from '$lib/agents';

	const REFRESH_MS = 5000;

	let pools = $state<Pool[]>([]);
	let agents = $state<Agent[]>([]);
	let moreAgents = $state(false);
	let morePools = $state(false);
	let loaded = $state(false);
	let error = $state<string | null>(null);

	const rows = $derived(
		pools.map((pool) => {
			const members = agents.filter((a) => a.pool_id === pool.id);
			const connected = members.filter((a) => a.status === 'connected');
			const capacity = connected.reduce((s, a) => s + a.capacity, 0);
			const free = connected.reduce((s, a) => s + (a.telemetry?.free_slots ?? 0), 0);
			return { pool, members, connected: connected.length, capacity, free };
		})
	);
	const poolName = (id: string) => pools.find((p) => p.id === id)?.name ?? id;

	async function refresh(signal: AbortSignal) {
		try {
			const [p, a] = await Promise.all([fetchPools(signal), fetchAgents(signal)]);
			if (signal.aborted) return;
			pools = p.items;
			morePools = p.more;
			agents = a.items;
			moreAgents = a.more;
			error = null;
		} catch (e) {
			if (signal.aborted) return;
			error = problemMessage(e);
		} finally {
			if (!signal.aborted) loaded = true;
		}
	}

	onMount(() => poll(refresh, REFRESH_MS));

	const time = (s?: string) => (s ? new Date(s).toLocaleString() : '-');
	const pct = (v?: number) => (v === undefined ? '-' : `${v.toFixed(1)}%`);
	const fixed = (v?: number) => (v === undefined ? '-' : v.toFixed(2));
</script>

<svelte:head>
	<title>Agents - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<h1 class="text-2xl font-semibold">Agents</h1>

	{#if error}
		<Alert.Root variant="destructive" data-testid="agents-error">
			<Alert.Title>Could not load agents</Alert.Title>
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !loaded}
		<div class="space-y-2" data-testid="agents-loading">
			<Skeleton class="h-24 w-full" />
			<Skeleton class="h-48 w-full" />
		</div>
	{:else if !error || agents.length > 0 || pools.length > 0}
		<Card.Root>
			<Card.Header>
				<Card.Title><h2 class="text-lg font-semibold">Pools</h2></Card.Title>
			</Card.Header>
			<Card.Content>
				{#if rows.length === 0}
					<p class="text-sm text-muted-foreground" data-testid="pools-empty">No pools yet.</p>
				{:else}
					<div class="overflow-x-auto">
						<Table.Root data-testid="pools-table">
							<Table.Header>
								<Table.Row>
									<Table.Head>Pool</Table.Head>
									<Table.Head>Agents connected</Table.Head>
									<Table.Head>Free / capacity</Table.Head>
								</Table.Row>
							</Table.Header>
							<Table.Body>
								{#each rows as r (r.pool.id)}
									<Table.Row>
										<Table.Cell class="font-medium">{r.pool.name}</Table.Cell>
										<Table.Cell>{r.connected} / {r.members.length}</Table.Cell>
										<Table.Cell>{r.free} / {r.capacity}</Table.Cell>
									</Table.Row>
								{/each}
							</Table.Body>
						</Table.Root>
					</div>
				{/if}

				{#if morePools}
					<p class="mt-2 text-sm text-muted-foreground" data-testid="pools-truncated">
						Showing the first {MAX_ROWS} pools; more exist.
					</p>
				{/if}
			</Card.Content>
		</Card.Root>

		<Card.Root>
			<Card.Header>
				<Card.Title><h2 class="text-lg font-semibold">Agents</h2></Card.Title>
			</Card.Header>
			<Card.Content>
				{#if agents.length === 0}
					<p class="text-sm text-muted-foreground" data-testid="agents-empty">
						No agents have connected yet.
					</p>
				{:else}
					<div class="overflow-x-auto">
						<Table.Root data-testid="agents-table">
							<Table.Header>
								<Table.Row>
									<Table.Head>Agent</Table.Head>
									<Table.Head>Status</Table.Head>
									<Table.Head>Pool</Table.Head>
									<Table.Head>Free / capacity</Table.Head>
									<Table.Head>CPU</Table.Head>
									<Table.Head>Memory</Table.Head>
									<Table.Head>Load</Table.Head>
									<Table.Head>Last seen</Table.Head>
									<Table.Head>Labels</Table.Head>
								</Table.Row>
							</Table.Header>
							<Table.Body>
								{#each agents as a (a.id)}
									{@const t = a.telemetry}
									<Table.Row data-testid="agent-row">
										<Table.Cell class="font-mono text-xs">{a.id}</Table.Cell>
										<Table.Cell>
											<Badge variant={a.status === 'connected' ? 'default' : 'secondary'}>
												{a.status}
											</Badge>
										</Table.Cell>
										<Table.Cell>{poolName(a.pool_id)}</Table.Cell>
										<Table.Cell>{t?.free_slots ?? '-'} / {a.capacity}</Table.Cell>
										<Table.Cell>{pct(t?.cpu_percent)}</Table.Cell>
										<Table.Cell>{formatBytes(t?.memory_bytes)}</Table.Cell>
										<Table.Cell>{fixed(t?.load1)}</Table.Cell>
										<Table.Cell>{time(a.last_seen)}</Table.Cell>
										<Table.Cell>
											<div class="flex flex-wrap gap-1">
												{#each Object.entries(a.labels) as [k, v] (k)}
													<Badge variant="outline">{k}={v}</Badge>
												{/each}
											</div>
										</Table.Cell>
									</Table.Row>
								{/each}
							</Table.Body>
						</Table.Root>
					</div>
				{/if}

				{#if moreAgents}
					<p class="mt-2 text-sm text-muted-foreground" data-testid="agents-truncated">
						Showing the first {MAX_ROWS} agents; more exist.
					</p>
				{/if}
			</Card.Content>
		</Card.Root>
	{/if}
</main>
