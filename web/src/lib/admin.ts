import { problemMessage } from '$lib/agents';

export { problemMessage };

const TOKEN_KEY = 'damson.admin_token';

/** The admin token lives in sessionStorage only: gone when the tab closes. */
export function loadToken(): string {
	try {
		return sessionStorage.getItem(TOKEN_KEY) ?? '';
	} catch {
		return '';
	}
}

export function saveToken(token: string): void {
	try {
		if (token) sessionStorage.setItem(TOKEN_KEY, token);
		else sessionStorage.removeItem(TOKEN_KEY);
	} catch {
		// storage blocked: the token then lasts for this page view only
	}
}

/** Thrown for a 401, so the page can ask for the token again. */
export class Unauthorized extends Error {}

export function headers(token: string) {
	return { Authorization: `Bearer ${token}` };
}

export function check(res: { response: Response; error?: unknown }): void {
	if (res.response.status === 401) throw new Unauthorized(problemMessage(res.error));
	if (res.error !== undefined) throw res.error;
}
