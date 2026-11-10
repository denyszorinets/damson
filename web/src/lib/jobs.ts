import { api, type components } from '$lib/api/client';
import { isNotFound, problemMessage } from '$lib/agents';

export { isNotFound, problemMessage };

export type Job = components['schemas']['Job'];

/** One page of the jobs the caller can see, of one project and below when scope is set. */
export async function fetchScopedJobs(
	scope: string,
	cursor = '',
	signal?: AbortSignal
): Promise<{ items: Job[]; next: string }> {
	const { data, error } = await api.GET('/tree/jobs', {
		params: { query: { scope: scope || undefined, cursor: cursor || undefined } },
		signal
	});
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return { items: data.items ?? [], next: data.next_cursor ?? '' };
}

/** The crumbs of a project path: each with the path it stands for. */
export function crumbs(scope: string): { label: string; path: string }[] {
	if (!scope) return [];
	const parts = scope.split('/');
	return parts.map((label, i) => ({ label, path: parts.slice(0, i + 1).join('/') }));
}

/** Jobs of the default project, newest page first; bounded like the other lists. */
export async function fetchJobs(signal?: AbortSignal): Promise<Job[]> {
	const { data, error } = await api.GET('/jobs', { params: { query: { limit: 200 } }, signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data.items ?? [];
}

/** A job and the graph of its current version. */
export async function fetchJobGraph(id: string, signal?: AbortSignal) {
	const detail = await api.GET('/jobs/{id}', { params: { path: { id } }, signal });
	if (detail.error !== undefined || !detail.data) throw detail.error ?? new Error('Empty response');
	const job = detail.data.job;
	const g = await api.GET('/jobs/{id}/versions/{v}/graph', {
		params: { path: { id, v: job.current_version } },
		signal
	});
	if (g.error !== undefined || !g.data) throw g.error ?? new Error('Empty response');
	return { job, graph: g.data, spec: detail.data.version.spec as unknown };
}
