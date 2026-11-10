import { api, type components } from '$lib/api/client';

export type PurgeResult = components['schemas']['PurgeLogsOutputBody'];

/** Removes every stored log chunk of one execution (instance admin; 409 while it can still write). */
export async function purgeExecutionLogs(id: string): Promise<PurgeResult> {
	const { data, error } = await api.DELETE('/executions/{id}/logs', {
		params: { path: { id } }
	});
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data;
}
