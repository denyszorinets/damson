/** A run of text with one style; the class is a fixed Tailwind class, never from the log. */
export interface AnsiSegment {
	text: string;
	cls: string;
}

const FG: Record<number, string> = {
	30: 'text-neutral-900 dark:text-neutral-300',
	31: 'text-red-600 dark:text-red-400',
	32: 'text-green-600 dark:text-green-400',
	33: 'text-yellow-700 dark:text-yellow-400',
	34: 'text-blue-600 dark:text-blue-400',
	35: 'text-fuchsia-600 dark:text-fuchsia-400',
	36: 'text-cyan-700 dark:text-cyan-400',
	37: 'text-neutral-500 dark:text-neutral-200'
};

// A CSI sequence: ESC [ params final byte. Only SGR (final "m") styles; the
// rest (cursor moves, erases) are dropped so they cannot garble the row.
// eslint-disable-next-line no-control-regex
const CSI = /\u001b\[([0-9;?]*)([ -/]*[@-~])/g;

/** Splits a line at its SGR colour codes; other escape sequences are removed. */
export function parseAnsi(line: string): AnsiSegment[] {
	const out: AnsiSegment[] = [];
	let fg = '';
	let bold = false;
	let last = 0;
	const emit = (text: string) => {
		if (text) out.push({ text, cls: [fg, bold ? 'font-bold' : ''].filter(Boolean).join(' ') });
	};
	for (const m of line.matchAll(CSI)) {
		emit(line.slice(last, m.index));
		last = m.index + m[0].length;
		if (m[2] !== 'm') continue;
		const codes = m[1] === '' ? [0] : m[1].split(';').map((c) => Number(c) || 0);
		for (const c of codes) {
			if (c === 0) {
				fg = '';
				bold = false;
			} else if (c === 1) bold = true;
			else if (c === 22) bold = false;
			else if (c === 39) fg = '';
			else if (FG[c]) fg = FG[c];
			else if (c >= 90 && c <= 97) fg = FG[c - 60];
		}
	}
	emit(line.slice(last));
	return out;
}
