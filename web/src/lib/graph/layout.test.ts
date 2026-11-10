import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import type { NodeState } from '$lib/runs';
import {
	badgesOf,
	layout,
	stepHeight,
	type Graph,
	type GraphNode,
	type PlacedNode
} from './layout';

// The graphs are the engine's golden outputs (testdata/engine/graph-*.golden.json),
// so the layout is checked against every shape BuildGraph produces.
const dir = join(__dirname, '../../../../testdata/engine');
const goldens = readdirSync(dir)
	.filter((f) => /^graph-.*\.golden\.json$/.test(f))
	.sort();

const load = (f: string) => JSON.parse(readFileSync(join(dir, f), 'utf8')) as Graph;

const overlap = (a: PlacedNode, b: PlacedNode) =>
	a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

describe('layout', () => {
	it('has graphs to check', () => {
		expect(goldens.length).toBeGreaterThanOrEqual(12);
	});

	for (const f of goldens) {
		describe(f, () => {
			const graph = load(f);
			const out = layout(graph);
			const byId = new Map(out.nodes.map((p) => [p.node.id, p]));

			it('places every node once, parents before children', () => {
				expect(out.nodes.map((p) => p.node.id).sort()).toEqual(
					graph.nodes!.map((n) => n.id).sort()
				);
				const seen = new Set<string>();
				for (const p of out.nodes) {
					if (p.parentId) expect(seen.has(p.parentId), `${p.node.id} before its parent`).toBe(true);
					seen.add(p.node.id);
				}
			});

			it('keeps children inside their parent box', () => {
				for (const p of out.nodes) {
					if (!p.parentId) continue;
					const parent = byId.get(p.parentId)!;
					expect(p.x).toBeGreaterThanOrEqual(0);
					expect(p.y).toBeGreaterThanOrEqual(0);
					expect(p.x + p.w, `${p.node.id} right edge`).toBeLessThanOrEqual(parent.w);
					expect(p.y + p.h, `${p.node.id} bottom edge`).toBeLessThanOrEqual(parent.h);
				}
			});

			it('does not overlap siblings', () => {
				const groups = new Map<string, PlacedNode[]>();
				for (const p of out.nodes)
					groups.set(p.parentId ?? '', [...(groups.get(p.parentId ?? '') ?? []), p]);
				for (const siblings of groups.values()) {
					for (let i = 0; i < siblings.length; i++)
						for (let j = i + 1; j < siblings.length; j++)
							expect(
								overlap(siblings[i], siblings[j]),
								`${siblings[i].node.id} / ${siblings[j].node.id}`
							).toBe(false);
				}
			});

			it('puts catch and finally to the right of the body', () => {
				for (const p of out.nodes) {
					if (p.node.kind !== 'catch' && p.node.kind !== 'finally') continue;
					const body = out.nodes.filter(
						(q) => q.parentId === p.parentId && (q.node.kind === 'step' || q.node.kind === 'group')
					);
					for (const b of body) expect(p.x).toBeGreaterThanOrEqual(b.x + b.w);
				}
			});

			it('is deterministic and bounded', () => {
				expect(layout(graph)).toEqual(out);
				expect(out.width).toBeGreaterThan(0);
				expect(out.height).toBeGreaterThan(0);
			});
		});
	}

	it('keeps every edge between placed nodes', () => {
		for (const f of goldens) {
			const graph = load(f);
			const ids = new Set(layout(graph).nodes.map((p) => p.node.id));
			for (const e of layout(graph).edges) {
				expect(ids.has(e.from) && ids.has(e.to), `${f}: ${e.from} -> ${e.to}`).toBe(true);
			}
		}
	});
});

// A chain of n steps, each with the badges that make a box taller.
function chain(n: number): { graph: Graph; overlay: Map<string, NodeState> } {
	const nodes: GraphNode[] = [{ id: '$start', kind: 'start', label: 'start' }];
	const overlay = new Map<string, NodeState>();
	for (let i = 0; i < n; i++) {
		const id = `step-${i}`;
		nodes.push({
			id,
			kind: 'step',
			label: id,
			plugin: 'ssh',
			when: i % 2 ? 'always()' : "env == 'prod' && version != ''",
			retry_max_attempts: 3,
			continue_on_error: i % 3 === 0
		} as GraphNode);
		overlay.set(id, {
			status: ['succeeded', 'failed', 'running', 'skipped'][i % 4],
			attempt: 1 + (i % 3),
			lanes: i % 5 === 0 ? { succeeded: 12, failed: 3, running: 1 } : {},
			laneTotal: i % 5 === 0 ? 16 : 0
		});
	}
	nodes.push({ id: '$end', kind: 'end', label: 'end' });
	return { graph: { nodes, edges: [] } as Graph, overlay };
}

describe('layout with badges', () => {
	it('grows a step with its badges and never shrinks below the base size', () => {
		const bare = { id: 'a', kind: 'step', label: 'a' } as GraphNode;
		const busy = chain(1).graph.nodes![1];
		expect(stepHeight(bare)).toBe(48);
		expect(stepHeight(busy, chain(1).overlay.get('step-0'))).toBeGreaterThan(stepHeight(busy));
		expect(badgesOf(busy).map((b) => b.text)).toContain('retry ×3');
	});

	for (let n = 1; n <= 20; n++) {
		it(`stacks ${n} steps with retry, when and status badges without overlap`, () => {
			const { graph, overlay } = chain(n);
			const out = layout(graph, overlay);
			const nodes = out.nodes;
			for (let i = 0; i < nodes.length; i++)
				for (let j = i + 1; j < nodes.length; j++)
					expect(overlap(nodes[i], nodes[j]), `${nodes[i].node.id} / ${nodes[j].node.id}`).toBe(
						false
					);
			const last = nodes[nodes.length - 1];
			expect(last.node.kind).toBe('end');
			expect(out.height).toBeGreaterThanOrEqual(last.y + last.h);
		});
	}
});
