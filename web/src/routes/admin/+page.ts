import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';

// The Admin area opens on its first page.
export const load = () => redirect(307, resolve('/admin/agents'));
