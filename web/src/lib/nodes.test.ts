import { describe, expect, it } from 'vitest';
import { emptyForm, formOf, parseForm, type Node } from './nodes';

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
