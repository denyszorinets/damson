import { describe, expect, it } from 'vitest';
import { nextIndex } from './keys';

describe('nextIndex', () => {
	it.each([
		[0, 3, 'ArrowDown', 1],
		[2, 3, 'ArrowDown', 0],
		[0, 3, 'ArrowUp', 2],
		[1, 3, 'ArrowRight', 2],
		[1, 3, 'ArrowLeft', 0],
		[2, 3, 'Home', 0],
		[0, 3, 'End', 2],
		[0, 3, 'a', null],
		[0, 0, 'ArrowDown', null]
	])('%d of %d on %s', (cur, n, key, want) => {
		expect(nextIndex(cur, n, key)).toBe(want);
	});
});
