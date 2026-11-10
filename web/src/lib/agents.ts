import { api, type components } from '$lib/api/client';

export type Agent = components['schemas']['Agent'];
export type Pool = components['schemas']['Pool'];

/** Pages fetched per refresh and rows rendered per table are bounded. */
const PAGE_SIZE = 200;
export const MAX_ROWS = 200;

export type Telemetry = NonNullable<Agent['telemetry']>;

/** Error text from a problem+json body, a plain-text body or a network failure. */
export function problemMessage(err: unknown): string {
	if (typeof err === 'string' && err.trim()) return err.trim().slice(0, 300);
	if (err && typeof err === 'object') {
		const p = err as { title?: string; detail?: string };
		if (p.detail) return p.title ? `${p.title}: ${p.detail}` : p.detail;
		if (p.title) return p.title;
	}
	return err instanceof Error ? err.message : 'Request failed';
}

export interface Listing<T> {
	items: T[];
	/** True when the server has more items than were fetched. */
	more: boolean;
}

interface Page<T> {
	data?: { items: T[] | null; next_cursor?: string };
	error?: unknown;
}

/** Follows next_cursor until MAX_ROWS items are collected. */
async function paginate<T>(fetchPage: (cursor?: string) => Promise<Page<T>>): Promise<Listing<T>> {
	const items: T[] = [];
	let cursor: string | undefined;
	for (;;) {
		const { data, error } = await fetchPage(cursor);
		if (error !== undefined || !data) throw error ?? new Error('Empty response');
		items.push(...(data.items ?? []));
		if (!data.next_cursor) return { items, more: false };
		if (items.length >= MAX_ROWS) return { items: items.slice(0, MAX_ROWS), more: true };
		cursor = data.next_cursor;
	}
}

export const fetchAgents = (signal?: AbortSignal) =>
	paginate<Agent>((cursor) =>
		api.GET('/agents', { params: { query: { limit: PAGE_SIZE, cursor } }, signal })
	);

export const fetchPools = (signal?: AbortSignal) =>
	paginate<Pool>((cursor) =>
		api.GET('/pools', { params: { query: { limit: PAGE_SIZE, cursor } }, signal })
	);

export function formatBytes(n: number | undefined): string {
	if (n === undefined) return '-';
	const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let i = 0;
	while (n >= 1024 && i < units.length - 1) {
		n /= 1024;
		i++;
	}
	return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}
