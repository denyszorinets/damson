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
	project: string;
	action: string;
	actor: string;
	/** A datetime-local value (UTC), or empty. */
	from: string;
	to: string;
	/** Leave out system:* actors (dispatch and other duties). */
	hideSystem: boolean;
}

export const emptyFilter = (): AuditFilter => ({
	targetType: '',
	target: '',
	project: '',
	action: '',
	actor: '',
	from: '',
	to: '',
	hideSystem: true
});

/** A datetime-local value read as UTC, as RFC 3339; empty or unparsable gives ''. */
export function utcTime(local: string): string {
	const v = local.trim();
	if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2})?$/.test(v)) return '';
	return `${v.length === 16 ? `${v}:00` : v}Z`;
}

type AuditQuery = {
	limit: number;
	target_type?: string;
	target?: string;
	project?: string;
	action?: string;
	actor?: string;
	from?: string;
	to?: string;
	hide_system?: boolean;
	cursor?: string;
};

/** The list query for a filter; empty fields are left out. */
export function auditQuery(filter: AuditFilter, cursor = ''): AuditQuery {
	const q: AuditQuery = { limit: 50 };
	if (filter.targetType.trim()) q.target_type = filter.targetType.trim();
	if (filter.target.trim()) q.target = filter.target.trim();
	if (filter.project.trim()) q.project = filter.project.trim();
	if (filter.action.trim()) q.action = filter.action.trim();
	if (filter.actor.trim()) q.actor = filter.actor.trim();
	if (utcTime(filter.from)) q.from = utcTime(filter.from);
	if (utcTime(filter.to)) q.to = utcTime(filter.to);
	if (filter.hideSystem) q.hide_system = true;
	if (cursor) q.cursor = cursor;
	return q;
}

/** The actor to show: the display name when the event holds one, else the id. */
export const actorLabel = (e: Pick<AuditEvent, 'actor' | 'actor_name'>): string =>
	e.actor_name || e.actor;

/** Distinct values of one field over the loaded events, sorted: the choices a filter offers. */
export function distinct(events: AuditEvent[], field: 'action' | 'actor'): string[] {
	return [...new Set(events.map((e) => e[field]))].sort();
}

/** One page of audit events, newest first. `cursor` continues a previous page. */
export async function fetchAudit(
	token: string,
	filter: AuditFilter,
	cursor = '',
	signal?: AbortSignal
): Promise<AuditPage> {
	const query = auditQuery(filter, cursor);
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
