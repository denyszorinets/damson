export interface DiffLine {
	kind: 'same' | 'added' | 'removed';
	text: string;
}

/** Larger inputs are not diffed: the table is quadratic. */
export const MAX_DIFF_LINES = 2000;

/**
 * A line diff of two texts by longest common subsequence. Returns null when
 * either text is longer than MAX_DIFF_LINES, so the caller can say so instead
 * of freezing the page.
 */
export function lineDiff(a: string, b: string): DiffLine[] | null {
	const x = a.split('\n');
	const y = b.split('\n');
	if (x.length > MAX_DIFF_LINES || y.length > MAX_DIFF_LINES) return null;
	// lcs[i][j]: length of the common subsequence of x[i..] and y[j..].
	const lcs: number[][] = Array.from({ length: x.length + 1 }, () =>
		new Array(y.length + 1).fill(0)
	);
	for (let i = x.length - 1; i >= 0; i--)
		for (let j = y.length - 1; j >= 0; j--)
			lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
	const out: DiffLine[] = [];
	let i = 0;
	let j = 0;
	while (i < x.length && j < y.length) {
		if (x[i] === y[j]) {
			out.push({ kind: 'same', text: x[i] });
			i++;
			j++;
		} else if (lcs[i + 1][j] >= lcs[i][j + 1]) out.push({ kind: 'removed', text: x[i++] });
		else out.push({ kind: 'added', text: y[j++] });
	}
	while (i < x.length) out.push({ kind: 'removed', text: x[i++] });
	while (j < y.length) out.push({ kind: 'added', text: y[j++] });
	return out;
}

/** A spec as the text that is compared: pretty-printed, so key order of the stored document stays. */
export function specText(spec: unknown): string {
	return JSON.stringify(spec, null, 2);
}
