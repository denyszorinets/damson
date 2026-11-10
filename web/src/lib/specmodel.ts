import type { SpecIssue } from '$lib/specedit';

/**
 * Pure helpers over a parsed job spec for the tabbed editor. Every edit takes a
 * spec and returns a new one; fields the editor does not know are left as they
 * are, so a form edit never drops a group, a catch block or a future field.
 */
// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Obj = Record<string, any>;
export type Spec = Obj;

export type EditorTab = 'details' | 'options' | 'steps' | 'json';

/** The parsed text when it is a JSON object, else null. */
export function readSpec(text: string): Spec | null {
	try {
		const v: unknown = JSON.parse(text);
		return v && typeof v === 'object' && !Array.isArray(v) ? (v as Spec) : null;
	} catch {
		return null;
	}
}

export const toText = (spec: Spec): string => JSON.stringify(spec, null, 2);

/** Sets or deletes (when empty) one key. */
function put(o: Obj, key: string, value: unknown): void {
	if (value === '' || value === undefined || value === false) delete o[key];
	else o[key] = value;
}

// Details

export interface Details {
	name: string;
	description: string;
	timeout: string;
	selector: string;
}

export function readDetails(spec: Spec): Details {
	return {
		name: spec.metadata?.name ?? '',
		description: spec.metadata?.description ?? '',
		timeout: spec.spec?.timeout ?? '',
		selector: spec.spec?.targets?.selector ?? ''
	};
}

export function setDetails(spec: Spec, d: Details): Spec {
	const next = structuredClone(spec);
	next.metadata = { ...next.metadata, name: d.name };
	put(next.metadata, 'description', d.description);
	next.spec = { ...next.spec };
	put(next.spec, 'timeout', d.timeout);
	if (d.selector) next.spec.targets = { ...next.spec.targets, selector: d.selector };
	else if (next.spec.targets) {
		delete next.spec.targets.selector;
		if (Object.keys(next.spec.targets).length === 0) delete next.spec.targets;
	}
	return next;
}

// Options

export type OptionType = 'string' | 'integer' | 'number' | 'boolean';
export const OPTION_TYPES: OptionType[] = ['string', 'integer', 'number', 'boolean'];

export interface OptionRow {
	name: string;
	type: OptionType;
	title: string;
	/** The default as text; empty means none. */
	default: string;
	required: boolean;
	/** The property has more than the form shows (enum, nested): kept as is. */
	advanced: boolean;
	raw?: Obj;
}

const SIMPLE = new Set(['type', 'title', 'default']);

export function optionRows(spec: Spec): OptionRow[] {
	const o = spec.spec?.options;
	const props: Obj = o?.properties && typeof o.properties === 'object' ? o.properties : {};
	const required: string[] = Array.isArray(o?.required) ? o.required : [];
	return Object.entries(props).map(([name, p]) => {
		const prop: Obj = p && typeof p === 'object' ? (p as Obj) : {};
		const advanced =
			!OPTION_TYPES.includes(prop.type) ||
			Object.keys(prop).some((k) => !SIMPLE.has(k)) ||
			(prop.default !== undefined && typeof prop.default === 'object');
		return {
			name,
			type: OPTION_TYPES.includes(prop.type) ? prop.type : 'string',
			title: typeof prop.title === 'string' ? prop.title : '',
			default: prop.default === undefined ? '' : String(prop.default),
			required: required.includes(name),
			advanced,
			raw: advanced ? prop : undefined
		};
	});
}

function parseDefault(type: OptionType, text: string): unknown {
	if (text === '') return undefined;
	if (type === 'integer' || type === 'number') {
		const n = Number(text);
		return Number.isFinite(n) ? n : text;
	}
	if (type === 'boolean') return text === 'true' ? true : text === 'false' ? false : text;
	return text;
}

export function setOptionRows(spec: Spec, rows: OptionRow[]): Spec {
	const next = structuredClone(spec);
	next.spec = { ...next.spec };
	if (rows.length === 0) {
		delete next.spec.options;
		return next;
	}
	const properties: Obj = {};
	for (const r of rows) {
		if (r.advanced && r.raw) {
			properties[r.name] = r.raw;
			continue;
		}
		const p: Obj = { type: r.type };
		put(p, 'title', r.title);
		const d = parseDefault(r.type, r.default);
		if (d !== undefined) p.default = d;
		properties[r.name] = p;
	}
	const options: Obj = { type: 'object', ...next.spec.options, properties };
	const required = rows.filter((r) => r.required).map((r) => r.name);
	if (required.length) options.required = required;
	else delete options.required;
	next.spec.options = options;
	return next;
}

// Steps

export interface StepPatch {
	id?: string;
	plugin?: string;
	timeout?: string;
	when?: string;
	continue_on_error?: boolean;
	/** Text of the retry attempts; empty removes the count. */
	max_attempts?: string;
	with?: Obj | undefined;
	/** Replaces the whole step. */
	whole?: Obj;
}

export interface StepRow {
	id: string;
	plugin: string;
	timeout: string;
	when: string;
	continue_on_error: boolean;
	max_attempts: string;
	with: Obj | undefined;
	/** The step has a group, runs_on, backoff or other field the form does not show. */
	complex: boolean;
	raw: Obj;
}

const FORM_KEYS = new Set([
	'id',
	'plugin',
	'with',
	'timeout',
	'when',
	'continue_on_error',
	'retry'
]);

const stepsOf = (spec: Spec): Obj[] => (Array.isArray(spec.spec?.steps) ? spec.spec.steps : []);

export function stepRows(spec: Spec): StepRow[] {
	return stepsOf(spec).map((s) => {
		const retry: Obj | undefined = s.retry && typeof s.retry === 'object' ? s.retry : undefined;
		const complex =
			s.type === 'group' ||
			Object.keys(s).some((k) => !FORM_KEYS.has(k) && !(k === 'type' && s[k] === 'plugin')) ||
			(retry !== undefined && Object.keys(retry).some((k) => k !== 'max_attempts'));
		return {
			id: s.id ?? '',
			plugin: s.plugin ?? '',
			timeout: s.timeout ?? '',
			when: s.when ?? '',
			continue_on_error: s.continue_on_error === true,
			max_attempts: retry?.max_attempts === undefined ? '' : String(retry.max_attempts),
			with: s.with && typeof s.with === 'object' ? s.with : undefined,
			complex,
			raw: s
		};
	});
}

function withSteps(spec: Spec, steps: Obj[]): Spec {
	return { ...spec, spec: { ...spec.spec, steps } };
}

export function updateStep(spec: Spec, i: number, patch: StepPatch): Spec {
	const steps = structuredClone(stepsOf(spec));
	if (i < 0 || i >= steps.length) return spec;
	if (patch.whole) {
		steps[i] = patch.whole;
		return withSteps(spec, steps);
	}
	const s = steps[i];
	for (const k of ['id', 'plugin', 'timeout', 'when', 'continue_on_error'] as const) {
		if (k in patch) put(s, k, patch[k]);
	}
	if ('with' in patch) put(s, 'with', patch.with);
	if ('max_attempts' in patch) {
		const text = patch.max_attempts ?? '';
		if (text !== '') {
			const n = Number(text);
			s.retry = { ...s.retry, max_attempts: Number.isFinite(n) ? n : text };
		} else if (s.retry) {
			delete s.retry.max_attempts;
			if (Object.keys(s.retry).length === 0) delete s.retry;
		}
	}
	return withSteps(spec, steps);
}

export function addStep(spec: Spec): Spec {
	const steps = stepsOf(spec);
	const taken = new Set(steps.map((s) => s.id));
	let n = steps.length + 1;
	while (taken.has(`step-${n}`)) n++;
	return withSteps(spec, [...structuredClone(steps), { id: `step-${n}`, plugin: '' }]);
}

export function removeStep(spec: Spec, i: number): Spec {
	return withSteps(
		spec,
		structuredClone(stepsOf(spec)).filter((_, n) => n !== i)
	);
}

export function moveStep(spec: Spec, i: number, by: -1 | 1): Spec {
	const steps = structuredClone(stepsOf(spec));
	const j = i + by;
	if (i < 0 || i >= steps.length || j < 0 || j >= steps.length) return spec;
	[steps[i], steps[j]] = [steps[j], steps[i]];
	return withSteps(spec, steps);
}

// Errors by tab

/** The tab that owns a server error path such as "spec.steps[0].plugin". */
export function tabOf(path: string): EditorTab {
	if (path.startsWith('metadata')) return 'details';
	if (/^spec\.(timeout|targets)(\.|\[|$)/.test(path)) return 'details';
	if (/^spec\.options(\.|\[|$)/.test(path)) return 'options';
	if (/^spec\.steps(\.|\[|$)/.test(path)) return 'steps';
	return 'json';
}

export function countByTab(issues: SpecIssue[]): Record<EditorTab, number> {
	const c: Record<EditorTab, number> = { details: 0, options: 0, steps: 0, json: 0 };
	for (const i of issues) c[tabOf(i.path)]++;
	return c;
}
