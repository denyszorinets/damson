import { describe, expect, it } from 'vitest';
import { breadcrumb, parseProjectRoute, projectRest } from './projectroute';

describe('parseProjectRoute', () => {
	it.each([
		[undefined, '', []],
		['', '', []],
		['acme', 'acme', []],
		['acme/pay', 'acme/pay', []],
		['acme/pay/', 'acme/pay', []],
		['acme/-/jobs', 'acme', ['jobs']],
		['acme/pay/-/jobs/abc', 'acme/pay', ['jobs', 'abc']],
		['acme/-', 'acme', []],
		['acme-co/x-y', 'acme-co/x-y', []],
		['/acme//-//activity', 'acme', ['activity']]
	])('%s', (rest, path, tab) => {
		expect(parseProjectRoute(rest)).toEqual({ path, tab });
	});
});

describe('projectRest', () => {
	it('round-trips with parseProjectRoute', () => {
		expect(projectRest('a/b')).toBe('a/b');
		expect(projectRest('a/b', 'jobs', 'x')).toBe('a/b/-/jobs/x');
		expect(parseProjectRoute(projectRest('a/b', 'jobs', 'x'))).toEqual({
			path: 'a/b',
			tab: ['jobs', 'x']
		});
	});
});

describe('breadcrumb', () => {
	it('lists each ancestor with its path', () => {
		expect(breadcrumb('a/b/c')).toEqual([
			{ path: 'a', label: 'a' },
			{ path: 'a/b', label: 'b' },
			{ path: 'a/b/c', label: 'c' }
		]);
		expect(breadcrumb('')).toEqual([]);
	});
});
