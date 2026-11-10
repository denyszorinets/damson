/** One log line of the SSE stream (event "log"). */
export interface LogLine {
	seq: number;
	ts: string;
	stream: string;
	line: string;
}

/** A range that will never arrive (event "gap"). */
export interface LogGap {
	seq_from: number;
	seq_to: number;
}

export type LogRow =
	| { kind: 'line'; seq: number; ts: string; stream: string; text: string }
	| { kind: 'gap'; from: number; to: number };

/** Rows kept in memory and offered to the list; older ones are dropped and counted. */
export const MAX_LOG_ROWS = 5000;

/**
 * The lines of one execution as the viewer holds them. A line is applied once:
 * the stream replays from the start after a reconnect, so a sequence number at or
 * below the last one is ignored. Memory is bounded: past MAX_LOG_ROWS the oldest
 * rows go (in chunks, so a flood costs little) and are counted in `dropped`.
 */
export class LogBuffer {
	rows: LogRow[] = [];
	/** The highest sequence number applied; resume from here. */
	lastSeq = 0;
	/** Rows dropped from the front to stay within the cap. */
	dropped = 0;
	/** Lines applied, dropped ones included. */
	total = 0;

	constructor(private readonly max = MAX_LOG_ROWS) {}

	/** Applies a line; false when it was already seen. */
	push(l: LogLine): boolean {
		if (l.seq <= this.lastSeq) return false;
		this.lastSeq = l.seq;
		this.total++;
		this.add({ kind: 'line', seq: l.seq, ts: l.ts, stream: l.stream, text: l.line });
		return true;
	}

	/** Marks a range that will not arrive; false when it lies behind what was applied. */
	gap(g: LogGap): boolean {
		if (g.seq_to <= this.lastSeq) return false;
		this.lastSeq = g.seq_to;
		this.add({ kind: 'gap', from: g.seq_from, to: g.seq_to });
		return true;
	}

	private add(row: LogRow) {
		this.rows.push(row);
		if (this.rows.length > this.max + Math.ceil(this.max / 10)) {
			const cut = this.rows.length - this.max;
			this.rows.splice(0, cut);
			this.dropped += cut;
		}
	}

	/** The rows to show: at most `max` of the newest. */
	view(): LogRow[] {
		return this.rows.length > this.max ? this.rows.slice(-this.max) : this.rows.slice();
	}

	get shown(): number {
		return Math.min(this.rows.length, this.max);
	}
}

/** The window of rows to render for a scroll position: fixed row height, with overscan. */
export function visibleWindow(
	count: number,
	scrollTop: number,
	viewport: number,
	rowHeight: number,
	overscan = 10
): { start: number; end: number } {
	const start = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
	const end = Math.min(count, Math.ceil((scrollTop + viewport) / rowHeight) + overscan);
	return { start, end: Math.max(start, end) };
}
