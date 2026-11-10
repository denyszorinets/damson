import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';

// The page moved to the Admin area.
export const load = () => redirect(307, resolve('/admin/service-accounts'));
