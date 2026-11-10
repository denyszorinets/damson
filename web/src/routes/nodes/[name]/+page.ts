import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';

// The page moved to the Admin area.
export const load = ({ params }: { params: { name: string } }) =>
	redirect(307, resolve('/admin/nodes/[name]', { name: params.name }));
