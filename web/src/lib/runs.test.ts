import { describe, expect, it } from 'vitest';
import { overlay, type Instance } from './runs';

const inst = (path: string, node: string, lane: string, status: string, attempt = 0): Instance => ({
	path,
	node,
	lane,
	status,
	attempt
});

describe('overlay', () => {
	it('keys a plain step by its node', () => {
		const m = overlay([inst('a', 'a', '', 'failed', 2)]);
		expect(m.get('a')).toMatchObject({ status: 'failed', attempt: 2, laneTotal: 0 });
	});

	it('counts the lanes of a for_each by status on the group and on the template node', () => {
		const m = overlay([
			inst('g', 'g', '', 'running'),
			inst('g/for_each[a]', 'g', 'a', 'succeeded'),
			inst('g/for_each[b]', 'g', 'b', 'running'),
			inst('g/for_each[a]/x', 'g/for_each[*]/x', 'a', 'succeeded', 1),
			inst('g/for_each[b]/x', 'g/for_each[*]/x', 'b', 'failed', 3)
		]);
		expect(m.get('g')).toMatchObject({
			status: 'running',
			laneTotal: 2,
			lanes: { succeeded: 1, running: 1 }
		});
		expect(m.get('g/for_each[*]/x')).toMatchObject({
			laneTotal: 2,
			attempt: 3,
			lanes: { succeeded: 1, failed: 1 }
		});
		expect(m.get('g/for_each[*]/x')?.status).toBeUndefined();
	});

	it('summarizes a thousand lanes without one entry per lane', () => {
		const list = Array.from({ length: 1000 }, (_, i) =>
			inst(`g/for_each[${i}]/x`, 'g/for_each[*]/x', String(i), i % 2 ? 'failed' : 'succeeded')
		);
		const m = overlay(list);
		expect(m.size).toBe(1);
		expect(m.get('g/for_each[*]/x')).toMatchObject({
			laneTotal: 1000,
			lanes: { failed: 500, succeeded: 500 }
		});
	});
});
