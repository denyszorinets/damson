import { describe, expect, it } from 'vitest';
import { describeSource, splitGroups } from './access';

describe('access helpers', () => {
	it('splits groups on commas, spaces and newlines', () => {
		expect(splitGroups('a, b\nc  d,,')).toEqual(['a', 'b', 'c', 'd']);
		expect(splitGroups('  ')).toEqual([]);
	});

	it('says where a role comes from', () => {
		expect(describeSource({ kind: 'direct', project: 'acme', rule_index: 0 })).toBe(
			'a rule of acme (rule 1)'
		);
		expect(describeSource({ kind: 'inherited', project: 'acme', rule_index: 2 })).toContain(
			'an ancestor (rule 3)'
		);
		expect(describeSource({ kind: 'job', rule_index: 0 })).toContain('the job itself');
		expect(describeSource({ kind: 'instance_admin', rule_index: -1 })).toContain('instance admin');
	});
});
