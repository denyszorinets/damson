import { describe, expect, it } from 'vitest';
import { MAX_DIFF_LINES, lineDiff, specText } from './diff';

const kinds = (d: ReturnType<typeof lineDiff>) => d?.map((l) => `${l.kind[0]}:${l.text}`);

describe('lineDiff', () => {
	it('marks the changed line as removed and added', () => {
		const a = specText({ steps: [{ id: 's', command: 'echo one' }] });
		const b = specText({ steps: [{ id: 's', command: 'echo two' }] });
		const d = lineDiff(a, b)!;
		expect(d.filter((l) => l.kind === 'removed').map((l) => l.text.trim())).toEqual([
			'"command": "echo one"'
		]);
		expect(d.filter((l) => l.kind === 'added').map((l) => l.text.trim())).toEqual([
			'"command": "echo two"'
		]);
		expect(d.filter((l) => l.kind === 'same').length).toBe(d.length - 2);
	});

	it('handles equal, empty and growing texts', () => {
		expect(kinds(lineDiff('a\nb', 'a\nb'))).toEqual(['s:a', 's:b']);
		expect(kinds(lineDiff('a', 'a\nb\nc'))).toEqual(['s:a', 'a:b', 'a:c']);
		expect(kinds(lineDiff('a\nb\nc', 'b'))).toEqual(['r:a', 's:b', 'r:c']);
		expect(kinds(lineDiff('', ''))).toEqual(['s:']);
	});

	it('reproduces both texts from the diff', () => {
		const a = 'x\ny\nz\nw';
		const b = 'x\nq\nz\nw\nv';
		const d = lineDiff(a, b)!;
		expect(
			d
				.filter((l) => l.kind !== 'added')
				.map((l) => l.text)
				.join('\n')
		).toBe(a);
		expect(
			d
				.filter((l) => l.kind !== 'removed')
				.map((l) => l.text)
				.join('\n')
		).toBe(b);
	});

	it('refuses inputs past the limit', () => {
		const big = new Array(MAX_DIFF_LINES + 1).fill('l').join('\n');
		expect(lineDiff(big, 'a')).toBeNull();
		expect(lineDiff('a', big)).toBeNull();
	});
});
