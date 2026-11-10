import { describe, expect, it } from 'vitest';
import { fieldsOf, rowsFor, type FieldRow } from './secrets';

const row = (name: string, value: string): FieldRow => ({
	name,
	value,
	required: false,
	multiline: false
});

describe('fieldsOf', () => {
	it('keeps rows with a name and a value, trimming names', () => {
		expect(fieldsOf([row(' user ', 'u'), row('pw', 'p')])).toEqual({ user: 'u', pw: 'p' });
	});
	it('leaves out unset optional fields and unnamed rows', () => {
		expect(fieldsOf([row('a', ''), row('', 'x'), row('b', 'y')])).toEqual({ b: 'y' });
	});
});

describe('rowsFor', () => {
	it('gives one empty named-by-the-user row for the generic type', () => {
		expect(rowsFor({ name: 'generic' })).toEqual([
			{ name: '', value: '', required: false, multiline: false }
		]);
		expect(rowsFor(undefined)).toHaveLength(1);
	});
	it('gives one empty row per field of a typed secret, values never pre-filled', () => {
		const rows = rowsFor({
			name: 'ssh_key',
			fields: [
				{ name: 'username', required: false, multiline: false },
				{ name: 'private_key', required: true, multiline: true }
			]
		});
		expect(rows.map((r) => [r.name, r.value, r.required, r.multiline])).toEqual([
			['username', '', false, false],
			['private_key', '', true, true]
		]);
	});
});
