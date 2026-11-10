import { api, type components } from '$lib/api/client';

export type SpecIssue = components['schemas']['SpecIssue'];
type Graph = components['schemas']['Graph'];
type JobDetail = components['schemas']['JobDetail'];

/** What the server says about a spec: its findings and, when valid, its graph. */
export interface Validation {
	valid: boolean;
	errors: SpecIssue[];
	warnings: SpecIssue[];
	graph?: Graph;
}

/** The text is not JSON, or the server refused the write with located errors. */
export class SpecRejected extends Error {
	constructor(readonly issues: SpecIssue[]) {
		super(issues.map((i) => `${i.path}: ${i.message}`).join('; '));
	}
}

/** Parses the editor's text; throws SpecRejected with the place of the syntax error. */
export function parseSpec(text: string): unknown {
	try {
		return JSON.parse(text);
	} catch (e) {
		throw new SpecRejected([
			{ path: '(document)', message: e instanceof Error ? e.message : 'not valid JSON' }
		]);
	}
}

/** Reads located errors ("spec.steps[0].plugin") out of a problem+json answer. */
export function issuesOf(problem: unknown): SpecIssue[] {
	const list = (
		problem as { errors?: { message?: string; location?: string }[] | null } | undefined
	)?.errors;
	return (Array.isArray(list) ? list : []).map((e) => ({
		path: (e.location ?? '').replace(/^body\./, ''),
		message: e.message ?? 'invalid'
	}));
}

/** Asks the server to check the spec (on demand): its errors by path and its graph. */
export async function validateSpec(text: string, signal?: AbortSignal): Promise<Validation> {
	const spec = parseSpec(text);
	const { data, error } = await api.POST('/jobs/validate', { body: { spec }, signal });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return {
		valid: data.valid,
		errors: data.errors ?? [],
		warnings: data.warnings ?? [],
		graph: data.graph
	};
}

function refused(error: unknown): never {
	const issues = issuesOf(error);
	if (issues.length) throw new SpecRejected(issues);
	throw error ?? new Error('Empty response');
}

/** Creates a job from the spec; resolves to its id. */
export async function createJob(text: string): Promise<string> {
	const { data, error } = await api.POST('/jobs', { body: { spec: parseSpec(text) } });
	if (error !== undefined || !data) refused(error);
	return data.job.id;
}

/** Adds a version based on `expected`; a stale one is a 409 the caller shows. */
export async function createVersion(id: string, text: string, expected: number): Promise<number> {
	const { data, error } = await api.POST('/jobs/{id}/versions', {
		params: { path: { id } },
		body: { spec: parseSpec(text), expected_version: expected }
	});
	if (error !== undefined || !data) refused(error);
	return data.version.version;
}

/** A job and its current spec as editable text. */
export async function loadForEdit(id: string): Promise<{ job: JobDetail['job']; text: string }> {
	const { data, error } = await api.GET('/jobs/{id}', { params: { path: { id } } });
	if (error !== undefined || !data) throw error ?? new Error('Empty response');
	return { job: data.job, text: JSON.stringify(data.version.spec, null, 2) };
}

/** A starting spec for a new job in `project`. */
export function newJobTemplate(project: string): string {
	return JSON.stringify(
		{
			apiVersion: 'damson.dev/v1',
			kind: 'Job',
			metadata: { project: project || 'default', name: 'new-job', description: '' },
			spec: {
				steps: [{ id: 'hello', plugin: 'ssh', with: { node: 'web-01', command: 'echo hello' } }]
			}
		},
		null,
		2
	);
}
