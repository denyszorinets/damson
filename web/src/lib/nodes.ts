import { api, type components } from '$lib/api/client';
import { check, headers, problemMessage } from '$lib/admin';

export { problemMessage };
export { Unauthorized, loadToken, saveToken } from '$lib/admin';

export type Node = components['schemas']['Node'];

export interface NodeForm {
	name: string;
	address: string;
	port: string;
	transport: string;
	os: string;
	pool: string;
	tags: string;
	attributes: string;
	credential: string;
	host_key: string;
	host_key_policy: string;
	enabled: boolean;
}

export const emptyForm = (): NodeForm => ({
	name: '',
	address: '',
	port: '',
	transport: 'ssh',
	os: '',
	pool: '',
	tags: '',
	attributes: '',
	credential: '',
	host_key: '',
	host_key_policy: 'strict',
	enabled: true
});

export function formOf(n: Node): NodeForm {
	return {
		name: n.name,
		address: n.address,
		port: String(n.port),
		transport: n.transport,
		os: n.os,
		pool: n.pool,
		tags: (n.tags ?? []).join(', '),
		attributes: Object.keys(n.attributes ?? {}).length ? JSON.stringify(n.attributes) : '',
		credential: n.credential,
		host_key: n.host_key,
		host_key_policy: n.host_key_policy,
		enabled: n.enabled
	};
}

/** Parsed free-text fields; a thrown Error is shown to the user. */
export function parseForm(f: NodeForm) {
	const tags = f.tags
		.split(',')
		.map((t) => t.trim())
		.filter(Boolean);
	let attributes: Record<string, unknown> = {};
	if (f.attributes.trim()) {
		try {
			const v = JSON.parse(f.attributes);
			if (v === null || typeof v !== 'object' || Array.isArray(v)) throw new Error();
			attributes = v as Record<string, unknown>;
		} catch {
			throw new Error('Attributes must be a JSON object, for example {"env": "prod"}');
		}
	}
	const port = f.port.trim() ? Number(f.port) : undefined;
	if (port !== undefined && !Number.isInteger(port)) throw new Error('Port must be a number');
	return { tags, attributes, port };
}

export interface NodePage {
	items: Node[];
	next?: string;
}

export async function fetchNodes(
	token: string,
	query: { source?: string; enabled?: string; selector?: string; cursor?: string },
	signal?: AbortSignal
): Promise<NodePage> {
	const res = await api.GET('/nodes', {
		params: {
			query: {
				limit: 50,
				source: query.source || undefined,
				enabled: (query.enabled || undefined) as 'true' | 'false' | undefined,
				selector: query.selector || undefined,
				cursor: query.cursor || undefined
			}
		},
		headers: headers(token),
		signal
	});
	check(res);
	return { items: res.data?.items ?? [], next: res.data?.next_cursor || undefined };
}

export async function fetchNode(token: string, name: string, signal?: AbortSignal): Promise<Node> {
	const res = await api.GET('/nodes/{name}', {
		params: { path: { name } },
		headers: headers(token),
		signal
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function createNode(token: string, f: NodeForm): Promise<Node> {
	const { tags, attributes, port } = parseForm(f);
	const res = await api.POST('/nodes', {
		headers: headers(token),
		body: {
			name: f.name.trim(),
			address: f.address.trim(),
			port,
			transport: f.transport,
			os: f.os.trim() || undefined,
			pool: f.pool.trim() || undefined,
			tags,
			attributes,
			credential: f.credential.trim() || undefined,
			host_key: f.host_key.trim() || undefined,
			host_key_policy: f.host_key_policy,
			enabled: f.enabled
		}
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function patchNode(
	token: string,
	name: string,
	f: NodeForm,
	ifVersion: number,
	force: boolean
): Promise<Node> {
	const { tags, attributes, port } = parseForm(f);
	const res = await api.PATCH('/nodes/{name}', {
		params: { path: { name }, query: { force } },
		headers: headers(token),
		body: {
			if_version: ifVersion,
			address: f.address.trim(),
			port,
			transport: f.transport,
			os: f.os.trim(),
			pool: f.pool.trim(),
			tags,
			attributes,
			credential: f.credential.trim(),
			host_key: f.host_key.trim(),
			host_key_policy: f.host_key_policy,
			enabled: f.enabled
		}
	});
	check(res);
	if (!res.data) throw new Error('Empty response');
	return res.data;
}

export async function deleteNode(
	token: string,
	name: string,
	ifVersion: number,
	force: boolean
): Promise<void> {
	const res = await api.DELETE('/nodes/{name}', {
		params: { path: { name }, query: { force, if_version: ifVersion } },
		headers: headers(token)
	});
	check(res);
}

export async function forgetHostKey(token: string, name: string): Promise<void> {
	const res = await api.DELETE('/nodes/{name}/host-key', {
		params: { path: { name } },
		headers: headers(token)
	});
	check(res);
}

export type RunTarget = components['schemas']['RunTarget'];

export async function fetchRunTargets(
	token: string,
	runId: string,
	cursor?: string,
	signal?: AbortSignal
): Promise<{ items: RunTarget[]; next?: string }> {
	const res = await api.GET('/runs/{id}/targets', {
		params: { path: { id: runId }, query: { limit: 50, cursor: cursor || undefined } },
		headers: headers(token),
		signal
	});
	check(res);
	return { items: res.data?.items ?? [], next: res.data?.next_cursor || undefined };
}

/** Fields a selector clause can test: one string field of a node, a tag, or an attribute. */
export type ClauseField = 'name' | 'os' | 'transport' | 'pool' | 'tag' | 'attribute';

const quote = (v: string) => `'${v.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
const IDENT = /^[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * One clause of a node selector, or null when the input cannot make one (an empty
 * value, or an attribute key that is not an identifier). The value is quoted and
 * escaped, so it cannot add to the expression.
 */
export function clause(field: ClauseField, value: string, key = ''): string | null {
	if (value === '') return null;
	switch (field) {
		case 'tag':
			return `${quote(value)} in node.tags`;
		case 'attribute':
			return IDENT.test(key) ? `node.attributes.${key} == ${quote(value)}` : null;
		default:
			return `node.${field} == ${quote(value)}`;
	}
}

/** Adds a clause to a selector with &&. */
export function addClause(selector: string, c: string): string {
	return selector.trim() ? `${selector.trim()} && ${c}` : c;
}

export interface SavedSelector {
	name: string;
	selector: string;
}

const SAVED_KEY = 'damson.nodes.selectors';

/** Selectors saved in this browser. Storage may be blocked or hold garbage. */
export function loadSaved(): SavedSelector[] {
	try {
		const list: unknown = JSON.parse(localStorage.getItem(SAVED_KEY) ?? '[]');
		return (Array.isArray(list) ? list : []).filter(
			(x): x is SavedSelector => !!x && typeof x.name === 'string' && typeof x.selector === 'string'
		);
	} catch {
		return [];
	}
}

/** Saves under a name (replacing one of the same name); returns the new list. */
export function saveSelector(name: string, selector: string): SavedSelector[] {
	const n = name.trim();
	const list = loadSaved().filter((s) => s.name !== n);
	if (n && selector.trim()) list.push({ name: n, selector: selector.trim() });
	persist(list);
	return list;
}

export function deleteSelector(name: string): SavedSelector[] {
	const list = loadSaved().filter((s) => s.name !== name);
	persist(list);
	return list;
}

function persist(list: SavedSelector[]) {
	try {
		localStorage.setItem(SAVED_KEY, JSON.stringify(list));
	} catch {
		// storage blocked: saved selectors then last only until reload
	}
}
