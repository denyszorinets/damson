import { beforeEach, describe, expect, it } from 'vitest';
import {
	addClause,
	clause,
	deleteSelector,
	emptyForm,
	formOf,
	loadSaved,
	parseForm,
	saveSelector,
	type Node
} from './nodes';

describe('parseForm', () => {
	it('splits tags and parses attributes and port', () => {
		const f = {
			...emptyForm(),
			tags: ' prod, db ,,',
			attributes: '{"env":"prod","n":3}',
			port: '2222'
		};
		expect(parseForm(f)).toEqual({
			tags: ['prod', 'db'],
			attributes: { env: 'prod', n: 3 },
			port: 2222
		});
	});
	it('treats empty attributes and port as unset', () => {
		expect(parseForm(emptyForm())).toEqual({ tags: [], attributes: {}, port: undefined });
	});
	it('rejects attributes that are not an object, and a non-integer port', () => {
		expect(() => parseForm({ ...emptyForm(), attributes: '[1]' })).toThrow(/JSON object/);
		expect(() => parseForm({ ...emptyForm(), attributes: 'nope' })).toThrow(/JSON object/);
		expect(() => parseForm({ ...emptyForm(), port: '2x' })).toThrow(/Port/);
	});
});

describe('formOf', () => {
	it('round-trips a node into editable text', () => {
		const n = {
			name: 'web-1',
			address: '10.0.0.1',
			port: 22,
			transport: 'ssh',
			os: 'linux',
			pool: 'p',
			tags: ['a', 'b'],
			attributes: { env: 'prod' },
			credential: 'secret://_inventory/k',
			host_key: '',
			host_key_policy: 'strict',
			enabled: true
		} as unknown as Node;
		const f = formOf(n);
		expect(f.tags).toBe('a, b');
		expect(f.port).toBe('22');
		expect(parseForm(f).attributes).toEqual({ env: 'prod' });
	});
	it('copes with null tags and attributes from the API', () => {
		const f = formOf({
			name: 'n',
			address: 'a',
			port: 22,
			transport: 'ssh',
			tags: null,
			attributes: null
		} as unknown as Node);
		expect(f.tags).toBe('');
		expect(f.attributes).toBe('');
	});
});

describe('selector builder', () => {
	it('builds a clause per field', () => {
		expect(clause('os', 'linux')).toBe("node.os == 'linux'");
		expect(clause('tag', 'prod')).toBe("'prod' in node.tags");
		expect(clause('attribute', 'eu', 'region')).toBe("node.attributes.region == 'eu'");
		expect(clause('pool', 'default')).toBe("node.pool == 'default'");
	});

	it('refuses what cannot make a clause and escapes values', () => {
		expect(clause('os', '')).toBeNull();
		expect(clause('attribute', 'x', 'bad key')).toBeNull();
		expect(clause('attribute', 'x', "a'b")).toBeNull();
		expect(clause('name', "it's")).toBe("node.name == 'it\\'s'");
		// A value cannot end the quote and add to the expression.
		expect(clause('os', "x' || true || '")).toBe("node.os == 'x\\' || true || \\''");
	});

	it('joins clauses with &&', () => {
		expect(addClause('', "node.os == 'linux'")).toBe("node.os == 'linux'");
		expect(addClause("node.os == 'linux'", "'prod' in node.tags")).toBe(
			"node.os == 'linux' && 'prod' in node.tags"
		);
	});
});

describe('saved selectors', () => {
	beforeEach(() => {
		const data = new Map<string, string>();
		globalThis.localStorage = {
			getItem: (k: string) => data.get(k) ?? null,
			setItem: (k: string, v: string) => void data.set(k, v)
		} as Storage;
	});

	it('saves, replaces by name, and deletes', () => {
		saveSelector('linux prod', "node.os == 'linux'");
		saveSelector('eu', "node.attributes.region == 'eu'");
		saveSelector('linux prod', "node.os == 'linux' && 'prod' in node.tags");
		expect(loadSaved().map((s) => s.name)).toEqual(['eu', 'linux prod']);
		expect(loadSaved()[1].selector).toContain('prod');
		expect(deleteSelector('eu').map((s) => s.name)).toEqual(['linux prod']);
	});

	it('ignores an empty name or selector and survives garbage', () => {
		saveSelector('', 'x');
		saveSelector('n', '  ');
		expect(loadSaved()).toEqual([]);
		localStorage.setItem('damson.nodes.selectors', '[1,{"name":"a"},{"name":"b","selector":"c"}]');
		expect(loadSaved()).toEqual([{ name: 'b', selector: 'c' }]);
	});
});
