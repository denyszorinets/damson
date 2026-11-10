import { LoginDidNotStick, loadMe } from '$lib/session';
import type { LayoutLoad } from './$types';

export const ssr = false;
export const prerender = false;

// Who the browser is. With login configured a visitor without a session is
// sent to the identity provider here, before any page loads.
export const load: LayoutLoad = async ({ fetch }) => {
	try {
		return { me: await loadMe(fetch), loginError: null as string | null };
	} catch (e) {
		if (e instanceof LoginDidNotStick) return { me: null, loginError: e.message };
		throw e;
	}
};
