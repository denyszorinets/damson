import { describe, expect, it } from 'vitest';
import { projectMatcher } from './projectsearch';

describe('projectMatcher', () => {
	it('an empty query matches everything and filters nothing', () => {
		const m = projectMatcher('   ');
		expect(m.empty).toBe(true);
		expect(m.test('acme/pay')).toBe(true);
	});

	it.each([
		['pay', 'acme/payments', true],
		['PAY', 'acme/payments', true],
		['acme/pa', 'acme/payments', true],
		['ops', 'acme/payments', false],
		['/^acme$/', 'acme', true],
		['/^acme$/', 'acme/pay', false],
		['/pay(ments)?$/', 'acme/payments', true],
		['/PAY/', 'acme/pay', true],
		['/x/', 'acme', false]
	])('%s on %s', (q, path, want) => {
		expect(projectMatcher(q).test(path)).toBe(want);
	});

	it('an invalid regular expression reports the problem and matches nothing', () => {
		const m = projectMatcher('/(/');
		expect(m.error).toBeTruthy();
		expect(m.test('acme')).toBe(false);
	});

	it('g and y flags do not make matching depend on earlier paths', () => {
		const m = projectMatcher('/pay/g');
		expect(['a/pay', 'pay', 'pay'].map((p) => m.test(p))).toEqual([true, true, true]);
		const y = projectMatcher('/pay/y');
		expect(['pay', 'pay'].map((p) => y.test(p))).toEqual([true, true]);
	});

	it('flags m and s are accepted; a leading slash without a closing one is plain text', () => {
		expect(projectMatcher('/^pay$/m').test('pay')).toBe(true);
		expect(projectMatcher('/acme/pay').error).toBeNull();
		expect(projectMatcher('/acme/pay').test('/acme/pay/x')).toBe(true);
	});

	it('a slash inside a plain query is plain text', () => {
		expect(projectMatcher('a/b').test('a/b/c')).toBe(true);
	});
});
