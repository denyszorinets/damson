<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Badge } from '$lib/components/ui/badge';
	import type { NodeState } from '$lib/runs';
	import type { GraphNode } from './layout';

	let { data }: NodeProps = $props();
	const node = $derived((data as { node: GraphNode }).node);
	const state = $derived((data as { state?: NodeState }).state);
	const lanes = $derived(Object.entries(state?.lanes ?? {}));

	const tone: Record<string, string> = {
		succeeded: 'border-green-600 bg-green-50 dark:bg-green-950',
		failed: 'border-red-600 bg-red-50 dark:bg-red-950',
		running: 'border-blue-600 bg-blue-50 dark:bg-blue-950',
		canceled: 'border-amber-600 bg-amber-50 dark:bg-amber-950',
		skipped: 'opacity-60'
	};
	const container = $derived(
		node.kind === 'group' || node.kind === 'catch' || node.kind === 'finally'
	);
</script>

<div
	class={container
		? 'h-full w-full rounded-lg border border-dashed bg-muted/30'
		: node.kind === 'start' || node.kind === 'end'
			? 'rounded-full border bg-muted px-3 py-1 text-center text-xs'
			: `rounded-md border bg-card px-3 py-2 text-sm shadow-sm ${tone[state?.status ?? ''] ?? ''}`}
	data-testid="graph-node-{node.id}"
	data-kind={node.kind}
	data-status={state?.status ?? ''}
	data-attempt={state?.attempt ?? ''}
>
	<Handle type="target" position={Position.Top} isConnectable={false} />
	<div
		class={container
			? 'flex items-center gap-2 px-3 py-2 text-xs font-medium'
			: 'flex flex-col gap-1'}
	>
		<span class="truncate font-medium">{node.label}</span>
		{#if node.plugin}
			<span class="text-xs text-muted-foreground">{node.plugin}</span>
		{/if}
		<span class="flex flex-wrap gap-1">
			{#if node.for_each}
				<Badge variant="secondary" data-testid="graph-foreach-{node.id}"
					>for each: {node.for_each.expr}</Badge
				>
			{/if}
			{#if node.when}
				<Badge variant="outline">when: {node.when}</Badge>
			{/if}
			{#if node.retry_max_attempts}
				<Badge variant="outline">retry ×{node.retry_max_attempts}</Badge>
			{/if}
			{#if state?.status}
				<Badge variant="secondary" data-testid="graph-status-{node.id}">{state.status}</Badge>
			{/if}
			{#if state && state.attempt > 1}
				<Badge variant="outline">attempt {state.attempt}</Badge>
			{/if}
			{#if lanes.length}
				<Badge variant="outline" data-testid="graph-lanes-{node.id}">
					{state?.laneTotal} lanes: {lanes.map(([s, n]) => `${n} ${s}`).join(', ')}
				</Badge>
			{/if}
			{#if node.continue_on_error}
				<Badge variant="outline">continue on error</Badge>
			{/if}
		</span>
	</div>
	<Handle type="source" position={Position.Bottom} isConnectable={false} />
</div>
