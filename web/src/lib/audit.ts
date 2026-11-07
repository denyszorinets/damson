import { api, type components } from '$lib/api/client';
import { check, headers, problemMessage } from '$lib/admin';

export { problemMessage };
export { Unauthorized, loadToken, saveToken } from '$lib/admin';

export type AuditEvent = components['schemas']['AuditEvent'];

export interface AuditPage {
	items: AuditEvent[];
	next: string;
}

export interface AuditFilter {
	targetType: string;
	target: string;
}

/** One page of audit events, newest first. `cursor` continues a previous page. */
export async function fetchAudit(
	token: string,
	filter: AuditFilter,
	cursor = '',
	signal?: AbortSignal
): Promise<AuditPage> {
	const query: { limit: number; target_type?: string; target?: string; cursor?: string } = {
		limit: 50
	};
	if (filter.targetType.trim()) query.target_type = filter.targetType.trim();
	if (filter.target.trim()) query.target = filter.target.trim();
	if (cursor) query.cursor = cursor;
	const res = await api.GET('/audit', { params: { query }, headers: headers(token), signal });
	check(res);
	return { items: res.data?.items ?? [], next: res.data?.next_cursor ?? '' };
}

/** Details as one compact line. They never hold secret values (ADR-0053). */
export function detailsText(details: unknown): string {
	if (details === null || details === undefined) return '';
	const text = JSON.stringify(details);
	return text === '{}' ? '' : text;
}
