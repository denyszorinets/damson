import { describe, expect, it } from 'vitest';
import { atLeast, canManage, depth, leaf, parentOf, type Project } from './projects';

const p = (path: string, my_role?: string) => ({ path, my_role }) as Project;

describe('project helpers', () => {
	it('splits paths', () => {
		expect(leaf('a/b/c')).toBe('c');
		expect(leaf('a')).toBe('a');
		expect(depth('a/b/c')).toBe(2);
		expect(parentOf('a/b')).toBe('a');
		expect(parentOf('a')).toBe('');
	});

	it('compares roles', () => {
		expect(atLeast('admin', 'editor')).toBe(true);
		expect(atLeast('runner', 'editor')).toBe(false);
		expect(atLeast(undefined, 'viewer')).toBe(false);
	});

	it('manages a child only with admin on the parent', () => {
		const m = new Map([
			['team', p('team', 'admin')],
			['ops', p('ops', 'runner')]
		]);
		expect(canManage('team/web', m)).toBe(true);
		expect(canManage('ops/web', m)).toBe(false);
		expect(canManage('ghost/web', m)).toBe(false);
	});
});
