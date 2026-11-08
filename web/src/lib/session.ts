import { api, type components } from '$lib/api/client';

export type Me = components['schemas']['Me'];

/**
 * Who the browser is, as the server reports it (GET /api/v1/me). The session
 * cookie is HttpOnly, so the page never sees it; the CSRF token, which /me
 * returns, travels in a header on writes.
 */
let current: Me | null = null;

/** The value admin pages treat as "a token": the session cookie does the work. */
export const SESSION_TOKEN = 'session';

export function setMe(me: Me | null): void {
	current = me;
}

export function getMe(): Me | null {
	return current;
}

/** The CSRF token to send with writes, empty when not logged in. */
export function csrfToken(): string {
	return current?.csrf_token ?? '';
}

/** True when a logged-in user, not a bearer token, is making the requests. */
export function isUser(): boolean {
	return current?.kind === 'user';
}

/** The login URL that returns to where the user was. */
export function loginUrl(returnTo: string): string {
	return `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
}

const STAMP_KEY = 'damson.login_redirect';
const LOOP_WINDOW_MS = 10_000;
const TOKEN_KEY = 'damson.admin_token';

function stored(key: string): string {
	try {
		return sessionStorage.getItem(key) ?? '';
	} catch {
		return '';
	}
}

function store(key: string, value: string): void {
	try {
		sessionStorage.setItem(key, value);
	} catch {
		// storage blocked: the guard then cannot work, which only costs a redirect
	}
}

/** Thrown by loadMe when login keeps failing, so the layout can say why. */
export class LoginDidNotStick extends Error {
	constructor() {
		super(
			'Login did not stick: the browser did not keep the session cookie. Check that the site is served over HTTPS (or set auth.cookie_insecure for plain http in development).'
		);
	}
}

/**
 * Loads /me once. A 401 means login is on and there is no session: the browser
 * is sent to the identity provider and the promise never settles. A stored
 * admin token is tried first, so an operator can get in while the provider is
 * down. If a redirect to log in happened a moment ago and we are back without a
 * session, the cookie is being dropped: stop instead of looping. Any other
 * failure leaves the app usable without an identity (the pages report their own
 * errors).
 */
export async function loadMe(fetchFn: typeof fetch = fetch): Promise<Me | null> {
	const token = stored(TOKEN_KEY);
	const ask = (bearer: string) =>
		api.GET('/me', {
			fetch: (req: Request) => fetchFn(req),
			headers: bearer ? { Authorization: `Bearer ${bearer}` } : {}
		});
	let res;
	try {
		res = await ask(token);
		// A stored token that is no longer valid must not hide the session.
		if (token && res.response.status === 401) res = await ask('');
	} catch {
		return null;
	}
	if (res.response.status === 401) {
		const last = Number(stored(STAMP_KEY)) || 0; // '' (cleared) is 0
		if (Date.now() - last < LOOP_WINDOW_MS) throw new LoginDidNotStick();
		store(STAMP_KEY, String(Date.now()));
		window.location.assign(loginUrl(window.location.pathname + window.location.search));
		return new Promise<never>(() => {});
	}
	if (!res.data) return null;
	store(STAMP_KEY, ''); // a working session: the login redirect did its job
	setMe(res.data);
	return res.data;
}

/**
 * Ends the session and goes to the provider's logout page, or home. /auth/logout
 * is a browser endpoint, not an API operation (it is not in the OpenAPI document),
 * so it is called directly.
 */
export async function logout(): Promise<void> {
	const res = await fetch('/auth/logout', {
		method: 'POST',
		credentials: 'same-origin',
		headers: { 'X-CSRF-Token': csrfToken() }
	});
	let next = '/';
	if (res.ok) {
		const body = (await res.json().catch(() => ({}))) as { end_session_url?: string };
		if (body.end_session_url) next = body.end_session_url;
	}
	setMe(null);
	window.location.assign(next);
}
