import { api } from '$lib/api/client';

/** A plugin a step can name, with the inputs its latest version requires. */
export interface PluginChoice {
	name: string;
	title: string;
	required: string[];
}

/** The plugins the controller knows, by name; empty when the list cannot be read. */
export async function fetchPluginChoices(signal?: AbortSignal): Promise<PluginChoice[]> {
	const { data } = await api.GET('/plugins', { signal });
	return (data?.items ?? []).map((p) => {
		const versions = p.versions ?? [];
		const latest = versions[versions.length - 1];
		const req = (latest?.input_schema as { required?: unknown } | undefined)?.required;
		return {
			name: p.name,
			title: latest?.title ?? '',
			required: Array.isArray(req) ? req.filter((r): r is string => typeof r === 'string') : []
		};
	});
}
