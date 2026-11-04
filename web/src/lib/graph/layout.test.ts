import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { layout, type Graph, type PlacedNode } from './layout';

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
