<script lang="ts">
	import { Background, Controls, SvelteFlow, type Edge, type Node } from '@xyflow/svelte';
	import '@xyflow/svelte/dist/style.css';
	import GraphBox from './GraphBox.svelte';
	import { layout, type Graph } from './layout';
	import type { NodeState } from '$lib/runs';

	/** overlay is the state of a run's steps by node; absent for a job without a run. */
	let { graph, overlay }: { graph: Graph; overlay?: Map<string, NodeState> } = $props();

	const nodeTypes = { box: GraphBox };

	const laid = $derived(layout(graph));
	const nodes = $derived<Node[]>(
		laid.nodes.map((p) => {
			const container =
				p.node.kind === 'group' || p.node.kind === 'catch' || p.node.kind === 'finally';
			return {
				id: p.node.id,
				type: 'box',
				position: { x: p.x, y: p.y },
				parentId: p.parentId,
				data: { node: p.node, state: overlay?.get(p.node.id) },
				style: container ? `width:${p.w}px;height:${p.h}px` : `width:${p.w}px`,
				draggable: false,
				selectable: false,
				connectable: false,
				zIndex: container ? 0 : 1
			};
		})
	);
	const edges = $derived<Edge[]>(
		laid.edges.map((e, i) => ({
			id: `${i}:${e.from}->${e.to}`,
			source: e.from,
			target: e.to,
			type: 'smoothstep',
			label: e.label,
			class: `edge-${e.kind}`,
			selectable: false,
			focusable: false
		}))
	);
</script>

<div class="h-[70vh] w-full rounded-md border" data-testid="graph">
	<SvelteFlow
		{nodes}
		{edges}
		{nodeTypes}
		fitView
		nodesDraggable={false}
		nodesConnectable={false}
		elementsSelectable={false}
		minZoom={0.2}
		proOptions={{ hideAttribution: true }}
	>
		<Background />
		<Controls showLock={false} />
	</SvelteFlow>
</div>

<style>
	:global(.svelte-flow__edge.edge-skip .svelte-flow__edge-path) {
		stroke-dasharray: 5 4;
	}
	:global(.svelte-flow__edge.edge-catch .svelte-flow__edge-path) {
		stroke: var(--color-destructive, #dc2626);
	}
	:global(.svelte-flow__edge.edge-finally .svelte-flow__edge-path) {
		stroke: var(--color-muted-foreground, #6b7280);
		stroke-dasharray: 2 3;
	}
</style>
