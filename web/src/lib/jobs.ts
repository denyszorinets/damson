import { api, type components } from '$lib/api/client';
import { problemMessage } from '$lib/agents';

export { problemMessage };

export type Job = components['schemas']['Job'];

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
