import { beforeEach, describe, expect, it } from 'vitest';
import { ancestry, loadExpanded, saveExpanded } from './tree';

describe('tree helpers', () => {
	beforeEach(() => {
		const data = new Map<string, string>();
		globalThis.localStorage = {
			getItem: (k: string) => data.get(k) ?? null,
			setItem: (k: string, v: string) => void data.set(k, v)
		} as Storage;
	});

	it('lists the path and its ancestors', () => {
		expect(ancestry('acme/payments/api')).toEqual(['acme', 'acme/payments', 'acme/payments/api']);
		expect(ancestry('acme')).toEqual(['acme']);
	});

	it('remembers expanded paths per user', () => {
		saveExpanded('bob', new Set(['acme']));
		expect([...loadExpanded('bob')]).toEqual(['acme']);
		expect(loadExpanded('carol').size).toBe(0);
	});

	it('survives garbage and blocked storage', () => {
		localStorage.setItem('damson.tree.expanded:x', '{not json');
		expect(loadExpanded('x').size).toBe(0);
		globalThis.localStorage = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		} as unknown as Storage;
		expect(loadExpanded('x').size).toBe(0);
		expect(() => saveExpanded('x', new Set(['a']))).not.toThrow();
	});
});
