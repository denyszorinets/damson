import { api, type components } from '$lib/api/client';
import { check, headers, problemMessage } from '$lib/admin';

export { problemMessage };
export { Unauthorized, loadToken, saveToken } from '$lib/admin';

export type Schedule = components['schemas']['Schedule'];
export type Webhook = components['schemas']['Webhook'];
export type CreatedWebhook = components['schemas']['CreatedWebhook'];
export type ServiceAccount = components['schemas']['ServiceAccount'];
export type ServiceToken = components['schemas']['ServiceToken'];
export type CreatedServiceToken = components['schemas']['CreatedServiceToken'];

/** Starts a run of the job's current version; resolves to the run id. No token: runs are open before M8. */
export async function triggerRun(jobId: string, options: Record<string, unknown>): Promise<string> {
	const { data, error } = await api.POST('/jobs/{id}/runs', {
		params: { path: { id: jobId } },
		body: { options }
	});
	if (error !== undefined || !data) {
		const rejected = optionErrors(error);
		if (rejected) throw rejected;
		throw error ?? new Error('Empty response');
	}
	return data.id;
}

/** A 422 on the options: what the server found wrong, by option and in general. */
export class OptionsRejected extends Error {
	constructor(
		/** Messages by option name (the first segment after "options."). */
		readonly fields: Record<string, string[]>,
		/** Messages about the options as a whole, such as a missing required one. */
		readonly general: string[]
	) {
		super(
			[...general, ...Object.entries(fields).map(([k, v]) => `${k}: ${v.join(', ')}`)].join('; ')
		);
	}
}

/**
 * Reads a problem+json answer's located errors ("options.env") into an
 * OptionsRejected; null when it holds none about the options.
 */
export function optionErrors(problem: unknown): OptionsRejected | null {
	const list = (
		problem as { errors?: { message?: string; location?: string }[] | null } | undefined
	)?.errors;
	if (!Array.isArray(list)) return null;
	const fields: Record<string, string[]> = {};
	const general: string[] = [];
	for (const e of list) {
		const loc = e.location ?? '';
		if (!(loc === 'options' || loc === 'body.options' || loc.startsWith('options.'))) continue;
		const message = e.message ?? 'invalid';
		const name = loc.startsWith('options.') ? loc.slice('options.'.length).split('.')[0] : '';
		if (name) (fields[name] ??= []).push(message);
		else general.push(message);
	}
	return Object.keys(fields).length || general.length ? new OptionsRejected(fields, general) : null;
}

/** The options schema of a job spec, or null when it declares none. */
export function optionsSchema(spec: unknown): Record<string, unknown> | null {
	const s = (spec as { spec?: { options?: unknown } } | undefined)?.spec?.options;
	return s && typeof s === 'object' ? (s as Record<string, unknown>) : null;
}

/** Whether a schema has properties to ask for. */
export function hasFields(schema: Record<string, unknown> | null): boolean {
	const props = schema?.properties;
	return !!props && typeof props === 'object' && Object.keys(props).length > 0;
}

export async function fetchSchedules(token: string, jobId: string): Promise<Schedule[]> {
	const res = await api.GET('/schedules', {
		params: { query: { job_id: jobId, limit: 200 } },
		headers: headers(token)
	});
	check(res);
	return res.data?.items ?? [];
}

// shortcut: one page of 200 rows for a project's schedules or webhooks; a project with more is
// not expected, so the list says so instead of paging. Add a cursor loop when it is.
function capped<T>(items: T[], next: string | undefined): T[] {
	if (next) throw new Error('This project has more than 200 entries; open the job to see its own.');
	return items;
}

/** The schedules of the jobs of a project and the projects below it. */
export async function fetchProjectSchedules(token: string, project: string): Promise<Schedule[]> {
	const res = await api.GET('/schedules', {
		params: { query: { project, limit: 200 } },
		headers: headers(token)
	});
	check(res);
	return capped(res.data?.items ?? [], res.data?.next_cursor);
}

/** The webhooks (revoked ones too) of the jobs of a project and the projects below it. */
export async function fetchProjectWebhooks(token: string, project: string): Promise<Webhook[]> {
	const res = await api.GET('/webhooks', {
		params: { query: { project, limit: 200 } },
		headers: headers(token)
	});
	check(res);
	return capped(res.data?.items ?? [], res.data?.next_cursor);
}

export async function createSchedule(
	token: string,
	b: {
		job_id: string;
		name: string;
		cron: string;
		timezone: string;
		options: Record<string, unknown>;
	}
): Promise<Schedule> {
	const res = await api.POST('/schedules', { headers: headers(token), body: b });
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function setScheduleEnabled(
	token: string,
	id: string,
	enabled: boolean
): Promise<void> {
	const res = await api.PATCH('/schedules/{id}', {
		params: { path: { id } },
		headers: headers(token),
		body: { enabled }
	});
	check(res);
}

export async function deleteSchedule(token: string, id: string): Promise<void> {
	check(await api.DELETE('/schedules/{id}', { params: { path: { id } }, headers: headers(token) }));
}

export async function fetchWebhooks(token: string, jobId: string): Promise<Webhook[]> {
	const res = await api.GET('/webhooks', {
		params: { query: { job_id: jobId, limit: 200 } },
		headers: headers(token)
	});
	check(res);
	return res.data?.items ?? [];
}

export async function createWebhook(
	token: string,
	b: { job_id: string; name: string; options?: Record<string, unknown>; options_mapping?: string }
): Promise<CreatedWebhook> {
	const res = await api.POST('/webhooks', { headers: headers(token), body: b });
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function revokeWebhook(token: string, id: string): Promise<void> {
	check(await api.DELETE('/webhooks/{id}', { params: { path: { id } }, headers: headers(token) }));
}

export async function fetchServiceAccounts(token: string): Promise<ServiceAccount[]> {
	const res = await api.GET('/service-accounts', {
		params: { query: { limit: 200 } },
		headers: headers(token)
	});
	check(res);
	return res.data?.items ?? [];
}

export async function createServiceAccount(
	token: string,
	name: string,
	description: string
): Promise<ServiceAccount> {
	const res = await api.POST('/service-accounts', {
		headers: headers(token),
		body: { name, description }
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function deleteServiceAccount(token: string, id: string): Promise<void> {
	check(
		await api.DELETE('/service-accounts/{id}', {
			params: { path: { id } },
			headers: headers(token)
		})
	);
}

export async function fetchServiceTokens(
	token: string,
	accountId: string
): Promise<ServiceToken[]> {
	const res = await api.GET('/service-accounts/{id}/tokens', {
		params: { path: { id: accountId } },
		headers: headers(token)
	});
	check(res);
	return res.data?.items ?? [];
}

export async function createServiceToken(
	token: string,
	accountId: string,
	b: { name: string; project: string; role: 'viewer' | 'runner' | 'editor'; expires_at?: string }
): Promise<CreatedServiceToken> {
	const res = await api.POST('/service-accounts/{id}/tokens', {
		params: { path: { id: accountId } },
		headers: headers(token),
		body: b
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function revokeServiceToken(token: string, id: string): Promise<void> {
	check(await api.DELETE('/tokens/{id}', { params: { path: { id } }, headers: headers(token) }));
}
