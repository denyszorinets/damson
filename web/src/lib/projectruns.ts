import { api } from '$lib/api/client';
import { fetchScopedJobs, type Job } from '$lib/jobs';
import type { RunSummary } from '$lib/runs';

export const RUN_STATUSES = ['pending', 'running', 'succeeded', 'failed', 'canceled'] as const;
export type RunStatus = (typeof RUN_STATUSES)[number];

export interface RunQuery {
	status?: RunStatus | '';
	job?: string;
	cursor?: string;
	limit?: number;
}

/**
 * One page of the runs of a project and the projects below it, newest first.
 * The server keeps only what the caller may see; a project the caller holds no
 * role on answers 404, as a missing one does.
 */
export async function fetchProjectRuns(
	project: string,
	q: RunQuery = {},
	signal?: AbortSignal
): Promise<{ items: RunSummary[]; next: string }> {
	const { data, error } = await api.GET('/runs', {
		params: {
			query: {
				project,
				status: q.status || undefined,
				job: q.job || undefined,
				cursor: q.cursor || undefined,
				limit: q.limit
			}
		},
		signal
	});
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return { items: data.items ?? [], next: data.next_cursor ?? '' };
}

/** The jobs of a project and those below it (one page), by id: names for run rows and the job filter. */
export async function fetchProjectJobs(
	project: string,
	signal?: AbortSignal
): Promise<Map<string, Job>> {
	const page = await fetchScopedJobs(project, '', signal);
	return new Map(page.items.map((j) => [j.id, j]));
}

/** Share of finished runs that succeeded, as a whole percent; null with nothing finished. A canceled run is neither. */
export function successRate(runs: RunSummary[]): number | null {
	const ok = runs.filter((r) => r.status === 'succeeded').length;
	const bad = runs.filter((r) => r.status === 'failed').length;
	return ok + bad === 0 ? null : Math.round((100 * ok) / (ok + bad));
}
