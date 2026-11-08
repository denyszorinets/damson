import createClient from 'openapi-fetch';
import type { paths } from './schema';
import { csrfToken } from '$lib/session';

/**
 * The typed API client. Types come from the controller's OpenAPI document
 * (make gen); do not write fetch calls by hand.
 */
export const api = createClient<paths>({ baseUrl: '/api/v1', fetch: withCsrf });

const SAFE = new Set(['GET', 'HEAD', 'OPTIONS']);

/**
 * Adds the CSRF token to writes made with the session cookie. Requests that
 * carry a bearer token are not cookie-authenticated, so the header is harmless.
 */
function withCsrf(request: Request): Promise<Response> {
	const token = csrfToken();
	if (token && !SAFE.has(request.method)) request.headers.set('X-CSRF-Token', token);
	return fetch(request);
}

export type { components } from './schema';
