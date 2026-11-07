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
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return data.id;
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
