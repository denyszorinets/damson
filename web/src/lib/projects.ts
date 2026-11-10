import { api, type components } from '$lib/api/client';
import { problemMessage } from '$lib/agents';
import { getMe } from '$lib/session';

export { problemMessage };

export type Project = components['schemas']['Project'];
export type AccessRule = components['schemas']['AccessRule'];
export type AccessRules = components['schemas']['AccessRules'];
export type NodeScope = components['schemas']['NodeScope'];

export const ROLES = ['viewer', 'runner', 'editor', 'admin'] as const;
export type Role = (typeof ROLES)[number];

const RANK: Record<string, number> = { viewer: 1, runner: 2, editor: 3, admin: 4 };

/** True when role is at least need. An absent role is below everything. */
export function atLeast(role: string | undefined, need: Role): boolean {
	return (RANK[role ?? ''] ?? 0) >= RANK[need];
}

/** The segment after the last "/": the name shown in the tree. */
export function leaf(path: string): string {
	return path.slice(path.lastIndexOf('/') + 1);
}

export function depth(path: string): number {
	return path.split('/').length - 1;
}

export function parentOf(path: string): string {
	const i = path.lastIndexOf('/');
	return i < 0 ? '' : path.slice(0, i);
}

/**
 * Whether the caller may create or delete the project at path: admin on its
 * parent, or an instance admin for a root (the server decides; this only hides
 * controls that would be refused).
 */
export function canManage(path: string, byPath: Map<string, Project>): boolean {
	const parent = parentOf(path);
	if (parent === '') return isInstanceAdmin();
	return atLeast(byPath.get(parent)?.my_role, 'admin');
}

export function isInstanceAdmin(): boolean {
	const me = getMe();
	return !me || me.kind !== 'user' || me.instance_admin;
}

function fail<T>(res: { data?: T; error?: unknown }): T {
	if (res.error !== undefined || res.data === undefined)
		throw res.error ?? new Error('Empty response');
	return res.data;
}

export async function fetchProjects(): Promise<Project[]> {
	return fail(await api.GET('/projects')).items ?? [];
}

/** One project the caller can see, or null when there is none (missing and no role read the same). */
export async function fetchProject(path: string): Promise<Project | null> {
	const res = await api.GET('/projects/{path}', { params: { path: { path } } });
	return res.data ?? null;
}

export async function createProject(path: string, description: string): Promise<Project> {
	return fail(await api.POST('/projects', { body: { path, description } }));
}

export async function deleteProject(path: string): Promise<void> {
	const res = await api.DELETE('/projects/{path}', { params: { path: { path } } });
	if (res.error !== undefined) throw res.error;
}

export async function fetchAccess(path: string): Promise<AccessRules> {
	return fail(await api.GET('/projects/{path}/access-rules', { params: { path: { path } } }));
}

export async function saveAccess(
	path: string,
	rules: AccessRule[],
	version: number
): Promise<AccessRules> {
	return fail(
		await api.PUT('/projects/{path}/access-rules', {
			params: { path: { path } },
			body: { rules, if_version: version }
		})
	);
}

export async function fetchNodeScope(path: string): Promise<NodeScope> {
	return fail(await api.GET('/projects/{path}/node-scope', { params: { path: { path } } }));
}

export async function saveNodeScope(
	path: string,
	nodeScope: string,
	version: number
): Promise<NodeScope> {
	return fail(
		await api.PUT('/projects/{path}/node-scope', {
			params: { path: { path } },
			body: { node_scope: nodeScope, if_version: version }
		})
	);
}
