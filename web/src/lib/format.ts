/** A duration in milliseconds as "850 ms", "12 s", "3 m 05 s" or "2 h 04 m"; "-" when unknown. */
export function formatDuration(ms: number | undefined | null): string {
	if (ms === undefined || ms === null || ms < 0) return '-';
	if (ms < 1000) return `${ms} ms`;
	const s = Math.round(ms / 1000);
	if (s < 60) return `${s} s`;
	const m = Math.floor(s / 60);
	if (m < 60) return `${m} m ${String(s % 60).padStart(2, '0')} s`;
	return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')} m`;
}

/** A timestamp for a table cell, or "-" when there is none. */
export function formatWhen(iso: string | undefined | null): string {
	return iso ? new Date(iso).toLocaleString() : '-';
}
