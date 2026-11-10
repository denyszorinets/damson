import { describe, expect, it } from 'vitest';
import {
	addStep,
	countByTab,
	moveStep,
	optionRows,
	readSpec,
	removeStep,
	setDetails,
	setOptionRows,
	stepRows,
	tabOf,
	updateStep,
	type Spec
} from './specmodel';

const rich = (): Spec => ({
	apiVersion: 'damson.dev/v1',
	kind: 'Job',
	metadata: { project: 'acme', name: 'j', description: 'd', extra: 1 },
	spec: {
		timeout: '30m',
		concurrency: { max: 1 },
		options: {
			type: 'object',
			required: ['env'],
			properties: {
				env: { type: 'string', title: 'Env', default: 'dev' },
				mode: { type: 'string', enum: ['a', 'b'] },
				n: { type: 'integer' }
			}
		},
		steps: [
			{
				id: 'a',
				plugin: 'ssh',
				with: { command: 'x' },
				retry: { max_attempts: 2, backoff: { initial: '1s' } },
				runs_on: { pool: 'p' }
			},
			{
				id: 'g',
				type: 'group',
				for_each: '[1]',
				steps: [{ id: 'b', plugin: 'ssh' }],
				catch: [{ id: 'c', plugin: 'ssh' }],
				finally: [{ id: 'd', plugin: 'ssh' }]
			}
		]
	}
});

describe('readSpec', () => {
	it('returns the object or null', () => {
		expect(readSpec('{"a":1}')).toEqual({ a: 1 });
		expect(readSpec('nope')).toBeNull();
		expect(readSpec('[1]')).toBeNull();
		expect(readSpec('3')).toBeNull();
	});
});

describe('details', () => {
	it('sets fields and keeps the rest', () => {
		const s = setDetails(rich(), { name: 'k', description: '', timeout: '', selector: 'true' });
		expect(s.metadata).toEqual({ project: 'acme', name: 'k', extra: 1 });
		expect(s.spec?.timeout).toBeUndefined();
		expect(s.spec?.targets).toEqual({ selector: 'true' });
		expect(s.spec?.concurrency).toEqual({ max: 1 });
		expect(s.spec?.steps).toHaveLength(2);
	});
});

describe('options', () => {
	it('reads rows in order with required and advanced flags', () => {
		const rows = optionRows(rich());
		expect(rows.map((r) => r.name)).toEqual(['env', 'mode', 'n']);
		expect(rows[0]).toMatchObject({ type: 'string', title: 'Env', default: 'dev', required: true });
		expect(rows[1].advanced).toBe(true);
		expect(rows[2].required).toBe(false);
	});

	it('writes rows back keeping advanced properties and parsing defaults by type', () => {
		const rows = optionRows(rich());
		rows[2] = { ...rows[2], default: '5', required: true };
		const s = setOptionRows(rich(), rows);
		const o = s.spec?.options as { required: string[]; properties: Record<string, unknown> };
		expect(o.properties.n).toEqual({ type: 'integer', default: 5 });
		expect(o.properties.mode).toEqual({ type: 'string', enum: ['a', 'b'] });
		expect(o.required).toEqual(['env', 'n']);
	});

	it('keeps a property with a non-scalar default whole', () => {
		const spec = rich();
		(spec.spec!.options.properties as Record<string, unknown>).l = {
			type: 'string',
			default: { a: 1 }
		};
		const rows = optionRows(spec);
		expect(rows[3].advanced).toBe(true);
		const next = setOptionRows(spec, rows);
		expect((next.spec!.options.properties as Record<string, unknown>).l).toEqual({
			type: 'string',
			default: { a: 1 }
		});
	});

	it('removes options when no rows are left', () => {
		expect(setOptionRows(rich(), []).spec?.options).toBeUndefined();
	});
});

describe('steps', () => {
	it('round-trips every edit without losing groups or unknown fields', () => {
		const before = rich();
		const after = updateStep(before, 0, { timeout: '5m' });
		const steps = after.spec?.steps as Record<string, unknown>[];
		expect(steps[0]).toMatchObject({ runs_on: { pool: 'p' }, timeout: '5m' });
		expect(steps[1]).toEqual(rich().spec?.steps?.[1]);
	});

	it('deletes cleared keys and keeps retry details when max_attempts is cleared', () => {
		const a = updateStep(rich(), 0, { timeout: '5m' });
		const b = updateStep(a, 0, { timeout: '' });
		expect((b.spec?.steps?.[0] as Record<string, unknown>).timeout).toBeUndefined();
		const c = updateStep(rich(), 0, { max_attempts: '' });
		expect((c.spec?.steps?.[0] as { retry?: unknown }).retry).toEqual({
			backoff: { initial: '1s' }
		});
		const d = updateStep(updateStep(rich(), 0, { max_attempts: '' }), 0, { max_attempts: '3' });
		expect((d.spec?.steps?.[0] as { retry?: { max_attempts: number } }).retry?.max_attempts).toBe(
			3
		);
		const e = updateStep(
			{ ...rich(), spec: { steps: [{ id: 'x', retry: { max_attempts: 2 } }] } },
			0,
			{ max_attempts: '' }
		);
		expect((e.spec?.steps?.[0] as { retry?: unknown }).retry).toBeUndefined();
	});

	it('adds, removes and moves', () => {
		let s = addStep(rich());
		expect(stepRows(s)).toHaveLength(3);
		expect(stepRows(s)[2].id).toMatch(/^step-/);
		s = moveStep(s, 2, -1);
		expect(stepRows(s)[1].id).toMatch(/^step-/);
		s = moveStep(s, 0, -1);
		expect(stepRows(s)[0].id).toBe('a');
		s = removeStep(s, 1);
		expect(stepRows(s).map((r) => r.id)).toEqual(['a', 'g']);
	});

	it('marks steps the form cannot show whole', () => {
		const rows = stepRows(rich());
		expect(rows[0].complex).toBe(true); // runs_on, backoff
		expect(rows[1].complex).toBe(true); // group
	});
});

describe('tabOf', () => {
	it('maps paths to tabs', () => {
		expect(tabOf('metadata.name')).toBe('details');
		expect(tabOf('spec.timeout')).toBe('details');
		expect(tabOf('spec.targets.selector')).toBe('details');
		expect(tabOf('spec.options.properties.x')).toBe('options');
		expect(tabOf('spec.steps[1].steps[0].id')).toBe('steps');
		expect(tabOf('(document)')).toBe('json');
		expect(tabOf('spec.concurrency')).toBe('json');
		expect(tabOf('')).toBe('json');
	});

	it('counts issues by tab', () => {
		const c = countByTab([
			{ path: 'spec.steps[0].plugin', message: 'x' },
			{ path: 'spec.steps[1].id', message: 'y' },
			{ path: 'metadata.name', message: 'z' }
		]);
		expect(c).toEqual({ details: 1, options: 0, steps: 2, json: 0 });
	});
});
