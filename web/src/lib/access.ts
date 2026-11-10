import { api, type components } from '$lib/api/client';

export type MyAccessItem = components['schemas']['MyAccessItem'];
export type AccessCheck = components['schemas']['AccessCheck'];
export type AccessSource = components['schemas']['AccessSource'];

/** The caller's own roles with where each comes from. */
export async function fetchMyAccess(signal?: AbortSignal): Promise<MyAccessItem[]> {
	const { data, error } = await api.GET('/access/me', { signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data.items ?? [];
}

/** Splits a list typed with commas, spaces or newlines into group names. */
export function splitGroups(text: string): string[] {
	return text
		.split(/[\s,]+/)
		.map((g) => g.trim())
		.filter(Boolean);
}

/** Asks why an identity has, or lacks, a role on a project or job (instance admin). */
export async function checkAccess(input: {
	groups: string[];
	email: string;
	project: string;
	jobId: string;
}): Promise<AccessCheck> {
	const { data, error } = await api.POST('/access/check', {
		body: {
			groups: input.groups,
			email: input.email || undefined,
			project: input.project || undefined,
			job_id: input.jobId || undefined
		}
	});
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data;
}

/** A sentence for where a role comes from. */
export function describeSource(s: AccessSource): string {
	switch (s.kind) {
		case 'direct':
			return `a rule of ${s.project} (rule ${s.rule_index + 1})`;
		case 'inherited':
			return `a rule of ${s.project}, an ancestor (rule ${s.rule_index + 1})`;
		case 'job':
			return `a rule of the job itself (rule ${s.rule_index + 1})`;
		case 'instance_admin':
			return 'the instance admin rule at login';
		case 'token':
			return s.project ? `the service token's scope ${s.project}` : "the service token's scope";
		default:
			return s.kind;
	}
}
