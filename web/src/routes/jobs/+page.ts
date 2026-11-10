import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';

// No screen lists jobs across projects; a project's own page lists its jobs.
export const load = () => redirect(307, resolve('/'));
