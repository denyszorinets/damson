import { describe, expect, it } from 'vitest';
import { parseAnsi } from './ansi';
import { LogBuffer, MAX_LOG_ROWS, visibleWindow } from './logs';

const line = (seq: number) => ({ seq, ts: '', stream: 'stdout', line: `l${seq}` });

describe('LogBuffer', () => {
	it('applies a line once, whatever the replay', () => {
		const b = new LogBuffer();
		for (const s of [1, 2, 3]) expect(b.push(line(s))).toBe(true);
		for (const s of [1, 2, 3, 3]) expect(b.push(line(s))).toBe(false);
		expect(b.push(line(4))).toBe(true);
		expect(b.rows.map((r) => (r.kind === 'line' ? r.seq : 0))).toEqual([1, 2, 3, 4]);
		expect(b.lastSeq).toBe(4);
	});

	it('keeps memory within the cap for a 100 000 line log', () => {
		const b = new LogBuffer();
		let peak = 0;
		for (let s = 1; s <= 100_000; s++) {
			b.push(line(s));
			peak = Math.max(peak, b.rows.length);
		}
		expect(b.total).toBe(100_000);
		expect(b.shown).toBe(MAX_LOG_ROWS);
		expect(peak).toBeLessThanOrEqual(MAX_LOG_ROWS * 1.1 + 1);
		expect(b.dropped + b.rows.length).toBe(100_000);
		const last = b.view().at(-1);
		expect(last && last.kind === 'line' && last.seq).toBe(100_000);
		expect(b.view()).toHaveLength(MAX_LOG_ROWS);
	});

	it('records a gap once and moves past it', () => {
		const b = new LogBuffer();
		b.push(line(1));
		expect(b.gap({ seq_from: 2, seq_to: 5 })).toBe(true);
		expect(b.gap({ seq_from: 2, seq_to: 5 })).toBe(false);
		expect(b.push(line(4))).toBe(false); // inside the gap
		expect(b.push(line(6))).toBe(true);
		expect(b.rows.map((r) => r.kind)).toEqual(['line', 'gap', 'line']);
	});
});

describe('LogBuffer level filter', () => {
	it('shows one stream, keeps gaps and the cap', () => {
		const b = new LogBuffer(10);
		for (let i = 1; i <= 6; i++)
			b.push({ seq: i, ts: '', stream: i % 2 ? 'stdout' : 'stderr', line: `l${i}` });
		b.gap({ seq_from: 7, seq_to: 8 });
		const seqs = (rows: ReturnType<LogBuffer['view']>) =>
			rows.map((r) => (r.kind === 'line' ? r.seq : 'gap'));
		expect(seqs(b.view('stderr'))).toEqual([2, 4, 6, 'gap']);
		expect(seqs(b.view('stdout'))).toEqual([1, 3, 5, 'gap']);
		expect(b.view('all')).toHaveLength(7);
		expect(b.view('stderr').length).toBeLessThanOrEqual(10);
	});
});

describe('window', () => {
	it('renders only what is visible plus overscan', () => {
		expect(visibleWindow(100_000, 0, 320, 20)).toEqual({ start: 0, end: 26 });
		expect(visibleWindow(100_000, 20_000, 320, 20, 5)).toEqual({ start: 995, end: 1021 });
		expect(visibleWindow(10, 0, 320, 20)).toEqual({ start: 0, end: 10 });
		expect(visibleWindow(0, 0, 320, 20)).toEqual({ start: 0, end: 0 });
	});
});

describe('parseAnsi', () => {
	it('colours and resets', () => {
		const s = parseAnsi('plain \u001b[31mred\u001b[0m \u001b[1;32mbold green\u001b[22m x');
		expect(s.map((x) => x.text)).toEqual(['plain ', 'red', ' ', 'bold green', ' x']);
		expect(s[1].cls).toContain('text-red-600');
		expect(s[3].cls).toContain('font-bold');
		expect(s[3].cls).toContain('text-green-600');
		expect(s[4].cls).toContain('text-green-600');
		expect(s[2].cls).toBe('');
	});

	it('drops other escape sequences and never keeps markup', () => {
		const s = parseAnsi('a\u001b[2K\u001b[1Gb<script>x</script>');
		expect(s.map((x) => x.text).join('')).toBe('ab<script>x</script>');
	});

	it('handles bright colours and a bare reset', () => {
		const s = parseAnsi('\u001b[91mx\u001b[my');
		expect(s[0].cls).toContain('text-red-600');
		expect(s[1].cls).toBe('');
	});
});
