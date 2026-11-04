<script lang="ts">
	import { Handle, Position, type NodeProps } from '@xyflow/svelte';
	import { Badge } from '$lib/components/ui/badge';
	import type { GraphNode } from './layout';

	let { data }: NodeProps = $props();
	const node = $derived((data as { node: GraphNode }).node);
	const container = $derived(
		node.kind === 'group' || node.kind === 'catch' || node.kind === 'finally'
	);
</script>

<div
	class={container
		? 'h-full w-full rounded-lg border border-dashed bg-muted/30'
		: node.kind === 'start' || node.kind === 'end'
			? 'rounded-full border bg-muted px-3 py-1 text-center text-xs'
			: 'rounded-md border bg-card px-3 py-2 text-sm shadow-sm'}
	data-testid="graph-node-{node.id}"
	data-kind={node.kind}
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
			{#if node.continue_on_error}
				<Badge variant="outline">continue on error</Badge>
			{/if}
		</span>
	</div>
	<Handle type="source" position={Position.Bottom} isConnectable={false} />
</div>
