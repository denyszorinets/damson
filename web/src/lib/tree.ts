import { api, type components } from '$lib/api/client';

export type TreeProject = components['schemas']['TreeProject'];
export type TreeJob = components['schemas']['TreeJob'];
export type SharedJob = components['schemas']['SharedJob'];

/** One expanded level: the child projects and, below a project, its jobs. */
export interface TreeLevel {
	projects: TreeProject[];
	jobs: TreeJob[];
	/** Cursors for the next page of projects and of jobs; empty when there is none. */
	next: string;
	jobsNext: string;
}

/** Loads one level of the caller's tree: the roots when parent is empty. */
export async function fetchLevel(parent: string, cursor = '', jobsCursor = ''): Promise<TreeLevel> {
	const res = await api.GET('/tree', {
		params: {
			query: {
				parent: parent || undefined,
				cursor: cursor || undefined,
				jobs_cursor: jobsCursor || undefined
			}
		}
	});
	if (res.data === undefined) throw res.error ?? new Error('Request failed');
	return {
		projects: res.data.projects ?? [],
		jobs: res.data.jobs ?? [],
		next: res.data.next_cursor ?? '',
		jobsNext: res.data.jobs_next_cursor ?? ''
	};
}

/** Jobs the caller reaches only through a rule of the job itself. */
export async function fetchShared(): Promise<{ items: SharedJob[]; truncated: boolean }> {
	const res = await api.GET('/tree/shared');
	if (res.data === undefined) throw res.error ?? new Error('Request failed');
	return { items: res.data.items ?? [], truncated: res.data.truncated ?? false };
}

const KEY = 'damson.tree.expanded';

/** The paths the user left expanded, kept per browser and user. Storage may be blocked. */
export function loadExpanded(sub: string): Set<string> {
	try {
		const raw = localStorage.getItem(`${KEY}:${sub}`);
		const list: unknown = raw ? JSON.parse(raw) : [];
		return new Set(Array.isArray(list) ? list.filter((x) => typeof x === 'string') : []);
	} catch {
		return new Set();
	}
}

export function saveExpanded(sub: string, open: Set<string>): void {
	try {
		localStorage.setItem(`${KEY}:${sub}`, JSON.stringify([...open]));
	} catch {
		// storage blocked: the tree then starts collapsed, which only costs a click
	}
}

/** The project paths on the way to path, itself included: what to open to show it. */
export function ancestry(path: string): string[] {
	const parts = path.split('/');
	return parts.map((_, i) => parts.slice(0, i + 1).join('/'));
}
