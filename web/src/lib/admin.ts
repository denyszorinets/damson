import { problemMessage } from '$lib/agents';
import { SESSION_TOKEN, isUser, loginUrl } from '$lib/session';

export { problemMessage };

const TOKEN_KEY = 'damson.admin_token';

/** The admin token lives in sessionStorage only: gone when the tab closes. */
export function loadToken(): string {
	if (isUser()) return SESSION_TOKEN; // a logged-in user: the cookie authenticates
	try {
		return sessionStorage.getItem(TOKEN_KEY) ?? '';
	} catch {
		return '';
	}
}

export function saveToken(token: string): void {
	if (token === SESSION_TOKEN) return;
	try {
		if (token) sessionStorage.setItem(TOKEN_KEY, token);
		else sessionStorage.removeItem(TOKEN_KEY);
	} catch {
		// storage blocked: the token then lasts for this page view only
	}
}

/** Thrown for a 401, so the page can ask for the token again. */
export class Unauthorized extends Error {}

export function headers(token: string): Record<string, string> {
	if (token === SESSION_TOKEN || token === '') return {}; // never send an empty bearer
	return { Authorization: `Bearer ${token}` };
}

export function check(res: { response: Response; error?: unknown }): void {
	if (res.response.status === 401 && isUser()) {
		// The session ended: log in again rather than asking a user for an admin token.
		window.location.assign(loginUrl(window.location.pathname + window.location.search));
	}
	if (res.response.status === 401) throw new Unauthorized(problemMessage(res.error));
	if (res.error !== undefined) throw res.error;
}
