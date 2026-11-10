<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import RunTargets from '$lib/components/RunTargets.svelte';
	import GraphView from '$lib/graph/GraphView.svelte';
	import type { Graph } from '$lib/graph/layout';
	import { isNotFound, problemMessage } from '$lib/jobs';
	import NotFound from '$lib/components/NotFound.svelte';
	import LogViewer from '$lib/components/LogViewer.svelte';
	import StepPanel from '$lib/components/StepPanel.svelte';
	import {
		fetchRunGraph,
		fromEvents,
		fromSnapshot,
		overlay,
		type Instance,
		type RunDetail,
		type RunStatusEvent,
		type StepEvent
	} from '$lib/runs';
	import { openStream, type StreamState } from '$lib/sse';

	/** Instances listed under the graph; more are counted, never rendered. */
	const MAX_ROWS = 200;

	const byPath = (list: Instance[]) => Object.fromEntries(list.map((i) => [i.path, i]));

	let graph = $state<Graph | null>(null);
	let instances = $state<Record<string, Instance>>({});
	let status = $state('');
	let conn = $state<StreamState>('connecting');
	let error = $state<string | null>(null);
	let missing = $state(false);
	let selected = $state<string | null>(null); // the step path whose panel is open
	let logOf = $state<string | null>(null); // the execution whose log is open

	const all = $derived(Object.values(instances));
	const nodeStates = $derived(overlay(all));
	const rows = $derived(all.slice(0, MAX_ROWS));
	const more = $derived(Math.max(0, all.length - MAX_ROWS));

	$effect(() => {
		const id = page.params.id ?? '';
		const ctrl = new AbortController();
		let closeStream: (() => void) | undefined;
		graph = null;
		error = null;
		missing = false;
		instances = {};
		status = '';

		fetchRunGraph(id, ctrl.signal)
			.then((r) => {
				if (ctrl.signal.aborted) return;
				graph = r.graph;
				instances = byPath(fromEvents(r.steps));
				closeStream = openStream(`/api/v1/runs/${encodeURIComponent(id)}/events`, {
					endOn: ['end'],
					onState: (s) => (conn = s),
					on: {
						// Every connection starts with a snapshot, which replaces what is known.
						snapshot: (d) => {
							const run = d as RunDetail;
							status = run.status;
							instances = byPath(fromSnapshot(run));
						},
						run: (d) => (status = (d as RunStatusEvent).status),
						step: (d) => {
							const s = d as StepEvent;
							instances = { ...instances, [s.path]: { ...s } };
						},
						end: (d) => (status = (d as RunStatusEvent).status)
					}
				});
			})
			.catch((e) => {
				if (ctrl.signal.aborted) return;
				if (isNotFound(e)) missing = true;
				else error = problemMessage(e);
			});
		return () => {
			ctrl.abort();
			closeStream?.();
		};
	});
</script>

<svelte:head>
	<title>Run - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-baseline gap-3">
		<a class="text-sm text-muted-foreground hover:text-foreground" href={resolve('/jobs')}>Jobs</a>
		<h1 class="text-2xl font-semibold">Run</h1>
		{#if status}<Badge data-testid="run-status">{status}</Badge>{/if}
		<span class="text-xs text-muted-foreground" data-testid="run-connection">{conn}</span>
	</div>

	{#if missing}
		<NotFound what="This run" />
	{:else if error}
		<Alert.Root variant="destructive" data-testid="run-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !graph}
		<Skeleton class="h-64 w-full" />
	{:else}
		<GraphView {graph} overlay={nodeStates} />

		<RunTargets runId={page.params.id ?? ''} />

		<Table.Root data-testid="instances-table">
			<Table.Header>
				<Table.Row>
					<Table.Head>Step</Table.Head>
					<Table.Head>Lane</Table.Head>
					<Table.Head>Status</Table.Head>
					<Table.Head>Attempt</Table.Head>
					<Table.Head>Execution</Table.Head>
					<Table.Head>Log</Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each rows as i (i.path)}
					<Table.Row data-testid="instance-row" data-path={i.path}>
						<Table.Cell class="font-mono text-xs">{i.path}</Table.Cell>
						<Table.Cell>{i.lane}</Table.Cell>
						<Table.Cell>{i.status}</Table.Cell>
						<Table.Cell>{i.attempt || ''}</Table.Cell>
						<Table.Cell>{i.state ?? ''}</Table.Cell>
						<Table.Cell>
							{#if i.execution_id}
								<button
									type="button"
									class="underline underline-offset-4"
									data-testid="open-log"
									onclick={() => {
										selected = i.path;
										logOf = i.execution_id ?? null;
									}}>Log</button
								>
							{/if}
						</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
		{#if selected}
			{@const inst = instances[selected]}
			<StepPanel
				runId={page.params.id ?? ''}
				path={selected}
				stamp={`${inst?.status}:${inst?.attempt}:${inst?.state}`}
				selected={logOf}
				onselect={(id) => (logOf = id)}
			/>
		{/if}
		{#if logOf}
			{#key logOf}<LogViewer executionId={logOf} />{/key}
		{/if}
		{#if more}
			<p class="text-sm text-muted-foreground" data-testid="instances-more">
				{more} more instances not shown.
			</p>
		{/if}
	{/if}
</main>
