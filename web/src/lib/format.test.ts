import { describe, expect, it } from 'vitest';
import { formatDuration, formatWhen } from './format';

describe('formatDuration', () => {
	it.each([
		[undefined, '-'],
		[null, '-'],
		[-1, '-'],
		[0, '0 ms'],
		[850, '850 ms'],
		[12_000, '12 s'],
		[59_600, '1 m 00 s'],
		[185_000, '3 m 05 s'],
		[7_440_000, '2 h 04 m']
	])('%s', (ms, want) => {
		expect(formatDuration(ms)).toBe(want);
	});
});

describe('formatWhen', () => {
	it('shows a dash with no time', () => {
		expect(formatWhen(undefined)).toBe('-');
		expect(formatWhen('')).toBe('-');
	});
});
