import { api, type components } from '$lib/api/client';

export type Agent = components['schemas']['Agent'];
export type Pool = components['schemas']['Pool'];

const MAX_PAGES = 20;

/** Error text from a problem+json body (or a network failure). */
export function problemMessage(err: unknown): string {
	if (err && typeof err === 'object') {
		const p = err as { title?: string; detail?: string };
		if (p.detail) return p.title ? `${p.title}: ${p.detail}` : p.detail;
		if (p.title) return p.title;
	}
	return err instanceof Error ? err.message : 'Request failed';
}

/** Fetches every page of agents through the generated client. */
export async function fetchAgents(): Promise<Agent[]> {
	const out: Agent[] = [];
	let cursor: string | undefined;
	for (let i = 0; i < MAX_PAGES; i++) {
		const { data, error } = await api.GET('/agents', {
			params: { query: { limit: 200, cursor } }
		});
		if (error) throw error;
		out.push(...(data.items ?? []));
		if (!data.next_cursor) break;
		cursor = data.next_cursor;
	}
	return out;
}

/** Fetches every page of pools through the generated client. */
export async function fetchPools(): Promise<Pool[]> {
	const out: Pool[] = [];
	let cursor: string | undefined;
	for (let i = 0; i < MAX_PAGES; i++) {
		const { data, error } = await api.GET('/pools', {
			params: { query: { limit: 200, cursor } }
		});
		if (error) throw error;
		out.push(...(data.items ?? []));
		if (!data.next_cursor) break;
		cursor = data.next_cursor;
	}
	return out;
}

function num(v: unknown): number | undefined {
	return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
}

export interface Telemetry {
	cpuPercent?: number;
	memoryBytes?: number;
	load1?: number;
	freeSlots?: number;
}

export function telemetryOf(a: Agent): Telemetry {
	const t = a.telemetry ?? {};
	return {
		cpuPercent: num(t.cpu_percent),
		memoryBytes: num(t.memory_bytes),
		load1: num(t.load1),
		freeSlots: num(t.free_slots)
	};
}

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
