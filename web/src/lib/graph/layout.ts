import type { components } from '$lib/api/client';
import type { NodeState } from '$lib/runs';

export type Graph = components['schemas']['Graph'];
export type GraphNode = components['schemas']['GraphNode'];
export type GraphEdge = components['schemas']['GraphEdge'];

/** Sizes and spacing of the box layout, in pixels. */
export const SIZE = {
	step: { w: 200, h: 48 },
	terminal: { w: 80, h: 32 },
	header: 36,
	pad: 16,
	gapY: 36,
	gapX: 40
} as const;

/** One badge of a step box; GraphBox renders exactly these and the layout sizes the box by them. */
export interface BadgeSpec {
	text: string;
	variant: 'secondary' | 'outline';
	testid?: string;
}

/** The badges of a node, from the server's graph and, for a run, the overlay of its instances. */
export function badgesOf(node: GraphNode, state?: NodeState): BadgeSpec[] {
	const out: BadgeSpec[] = [];
	const lanes = Object.entries(state?.lanes ?? {});
	if (node.for_each)
		out.push({
			text: `for each: ${node.for_each.expr}`,
			variant: 'secondary',
			testid: `graph-foreach-${node.id}`
		});
	if (node.when) out.push({ text: `when: ${node.when}`, variant: 'outline' });
	if (node.retry_max_attempts)
		out.push({ text: `retry ×${node.retry_max_attempts}`, variant: 'outline' });
	if (state?.status)
		out.push({
			text: state.status,
			variant: 'secondary',
			testid: `graph-status-${node.id}`
		});
	if (state && state.attempt > 1)
		out.push({ text: `attempt ${state.attempt}`, variant: 'outline' });
	if (lanes.length)
		out.push({
			text: `${state?.laneTotal} lanes: ${lanes.map(([st, n]) => `${n} ${st}`).join(', ')}`,
			variant: 'outline',
			testid: `graph-lanes-${node.id}`
		});
	if (node.continue_on_error) out.push({ text: 'continue on error', variant: 'outline' });
	return out;
}

/** The text metrics of a step box (text-sm label, text-xs badges), in pixels. */
const BOX = { padX: 24, padY: 16, line: 20, small: 16, badge: 20, gap: 4, char: 6.6, badgePad: 18 };

/**
 * The height of a step box: its label, plugin line and the rows its badges wrap
 * into. Badges are truncated to the box width, so a long one takes one row. An
 * estimate that errs tall: boxes may leave a little space, never overlap.
 */
export function stepHeight(node: GraphNode, state?: NodeState): number {
	const room = SIZE.step.w - BOX.padX;
	let rows = 0;
	let used = 0;
	for (const b of badgesOf(node, state)) {
		const w = Math.min(room, b.text.length * BOX.char + BOX.badgePad);
		if (rows === 0 || used + BOX.gap + w > room) {
			rows++;
			used = w;
		} else used += BOX.gap + w;
	}
	const sections = 1 + (node.plugin ? 1 : 0) + (rows ? 1 : 0);
	const h =
		BOX.padY +
		BOX.line +
		(node.plugin ? BOX.small : 0) +
		rows * BOX.badge +
		Math.max(0, rows - 1) * BOX.gap +
		(sections - 1) * BOX.gap +
		2; // the border
	return Math.max(SIZE.step.h, Math.ceil(h));
}

export interface PlacedNode {
	node: GraphNode;
	/** Position relative to the parent box (or the canvas for top-level nodes). */
	x: number;
	y: number;
	w: number;
	h: number;
	parentId?: string;
}

export interface Layout {
	nodes: PlacedNode[];
	edges: GraphEdge[];
	width: number;
	height: number;
}

interface Box {
	w: number;
	h: number;
	/** Placed descendants, positions relative to this box's origin. */
	placed: PlacedNode[];
}

/**
 * Lays the graph out as nested boxes. It only places what the server sent: the
 * nodes' kinds, parents and order come from engine.BuildGraph, and the layout
 * never reads the job spec. A step is as tall as its badges need, so a run's
 * status badges (the optional overlay) are counted. Lists stack top to bottom; a group wraps its body,
 * with its catch and finally lists as columns to its right.
 */
export function layout(graph: Graph, overlay?: Map<string, NodeState>): Layout {
	const kids = new Map<string, GraphNode[]>();
	for (const n of graph.nodes ?? []) {
		const key = n.parent ?? '';
		kids.set(key, [...(kids.get(key) ?? []), n]);
	}
	const children = (parent: string) => kids.get(parent) ?? [];

	// Lays out the nodes of one list, stacked, centered on the widest one.
	function stack(
		items: GraphNode[],
		parentId: string | undefined,
		originX: number,
		originY: number
	) {
		const boxes = items.map((n) => ({ n, box: place(n) }));
		const width = Math.max(0, ...boxes.map((b) => b.box.w));
		const placed: PlacedNode[] = [];
		let y = originY;
		for (const { n, box } of boxes) {
			placed.push(
				{ node: n, x: originX + (width - box.w) / 2, y, w: box.w, h: box.h, parentId },
				...box.placed
			);
			y += box.h + SIZE.gapY;
		}
		return { placed, w: width, h: boxes.length ? y - SIZE.gapY - originY : 0 };
	}

	// place sizes one node: a leaf has a fixed size, a container wraps its lists.
	function place(n: GraphNode): Box {
		if (n.kind === 'start' || n.kind === 'end') return { ...SIZE.terminal, placed: [] };
		if (n.kind === 'step')
			return { w: SIZE.step.w, h: stepHeight(n, overlay?.get(n.id)), placed: [] };
		const inner = children(n.id);
		const body = inner.filter((c) => c.kind === 'step' || c.kind === 'group');
		const columns = [
			inner.filter((c) => c.kind === 'catch'),
			inner.filter((c) => c.kind === 'finally')
		].flat();
		if (n.kind === 'catch' || n.kind === 'finally') {
			const list = stack(inner, n.id, SIZE.pad, SIZE.header);
			return {
				w: Math.max(SIZE.step.w, list.w) + 2 * SIZE.pad,
				h: SIZE.header + list.h + SIZE.pad,
				placed: list.placed
			};
		}
		// group: body column, then catch and finally columns.
		const bodyList = stack(body, n.id, SIZE.pad, SIZE.header);
		const placed = [...bodyList.placed];
		let x = SIZE.pad + bodyList.w + SIZE.gapX;
		let height = bodyList.h;
		for (const c of columns) {
			const box = place(c);
			placed.push(
				{ node: c, x, y: SIZE.header, w: box.w, h: box.h, parentId: n.id },
				...box.placed
			);
			x += box.w + SIZE.gapX;
			height = Math.max(height, box.h);
		}
		const width = columns.length ? x - SIZE.gapX + SIZE.pad : SIZE.pad + bodyList.w + SIZE.pad;
		return {
			w: Math.max(width, SIZE.step.w + 2 * SIZE.pad),
			h: SIZE.header + height + SIZE.pad,
			placed
		};
	}

	const top = stack(children(''), undefined, 0, 0);
	return { nodes: top.placed, edges: graph.edges ?? [], width: top.w, height: top.h };
}
