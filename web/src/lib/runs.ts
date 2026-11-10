import { api, type components } from '$lib/api/client';

export type StepEvent = components['schemas']['StepEvent'];
export type RunGraph = components['schemas']['RunGraph'];
export type RunDetail = components['schemas']['RunDetail'];

/** Payload of the run stream's "run" and "end" events. */
export interface RunStatusEvent {
	status: string;
	cancel_requested: boolean;
}

/** One step instance as the overlay knows it. */
export interface Instance {
	path: string;
	node: string;
	lane: string;
	status: string;
	attempt: number;
	state?: string;
	/** The latest attempt's execution, whose log can be opened. */
	execution_id?: string;
}

/** What the graph shows on a node: the node's own instance and its lanes. */
export interface NodeState {
	/** Status of the instance outside any lane, if there is one. */
	status?: string;
	attempt: number;
	state?: string;
	/** Instances in lanes by status (for_each), and how many there are. */
	lanes: Record<string, number>;
	laneTotal: number;
}

/** Instances of a snapshot (a RunDetail), in creation order. */
export function fromSnapshot(run: RunDetail): Instance[] {
	return (run.steps ?? []).map((s) => {
		const last = (s.executions ?? []).at(-1);
		return {
			path: s.path,
			node: s.node,
			lane: s.lane ?? '',
			status: s.status,
			attempt: last?.attempt ?? 0,
			state: last?.state,
			execution_id: last?.id
		};
	});
}

/** Instances of the run graph endpoint's steps. */
export function fromEvents(steps: StepEvent[] | null): Instance[] {
	return (steps ?? []).map((s) => ({
		path: s.path,
		node: s.node,
		lane: s.lane,
		status: s.status,
		attempt: s.attempt,
		state: s.state,
		execution_id: s.execution_id
	}));
}

/**
 * Groups instances by the node the server assigned them to. This only counts
 * server-supplied fields (node, lane, status); it derives no control flow.
 */
export function overlay(instances: Iterable<Instance>): Map<string, NodeState> {
	const out = new Map<string, NodeState>();
	for (const i of instances) {
		let n = out.get(i.node);
		if (!n) out.set(i.node, (n = { attempt: 0, lanes: {}, laneTotal: 0 }));
		if (i.lane === '') {
			n.status = i.status;
			n.attempt = i.attempt;
			n.state = i.state;
		} else {
			n.lanes[i.status] = (n.lanes[i.status] ?? 0) + 1;
			n.laneTotal++;
			// A template node's lanes may each have attempts; show the highest.
			n.attempt = Math.max(n.attempt, i.attempt);
		}
	}
	return out;
}

/** A run with its graph and the state of its steps. */
export async function fetchRunGraph(id: string, signal?: AbortSignal) {
	const { data, error } = await api.GET('/runs/{id}/graph', { params: { path: { id } }, signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data;
}

export const isTerminal = (status: string) => ['succeeded', 'failed', 'canceled'].includes(status);

export type StepDetail = NonNullable<RunDetail['steps']>[number];

/** A run with its step tree and each step's attempts. */
export async function fetchRunDetail(id: string, signal?: AbortSignal): Promise<RunDetail> {
	const { data, error } = await api.GET('/runs/{id}', { params: { path: { id } }, signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data;
}

export type RunSummary = components['schemas']['Run'];

/** The newest runs the caller may see, role-filtered by the server. */
export async function fetchRecentRuns(
	limit = 25,
	signal?: AbortSignal
): Promise<{ items: RunSummary[]; more: boolean }> {
	const { data, error } = await api.GET('/runs', { params: { query: { limit } }, signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	// A cursor with fewer runs than asked means the server stopped scanning, not that the list ended.
	return { items: data.items ?? [], more: !!data.next_cursor };
}

/** A job's name and project by id, for runs; the visible jobs first, then one read each. */
export async function jobsById(
	ids: string[],
	signal?: AbortSignal
): Promise<Map<string, { name: string; project: string }>> {
	const out = new Map<string, { name: string; project: string }>();
	const first = await api.GET('/tree/jobs', { params: { query: { limit: 200 } }, signal });
	for (const j of first.data?.items ?? []) out.set(j.id, { name: j.name, project: j.project });
	for (const id of new Set(ids)) {
		if (out.has(id)) continue;
		const one = await api.GET('/jobs/{id}', { params: { path: { id } }, signal });
		if (one.data) out.set(id, { name: one.data.job.name, project: one.data.job.project });
	}
	return out;
}
