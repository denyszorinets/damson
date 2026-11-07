import { describe, expect, it } from 'vitest';
import { detailsText } from './audit';

describe('detailsText', () => {
	it('leaves empty details blank', () => {
		expect(detailsText({})).toBe('');
		expect(detailsText(null)).toBe('');
		expect(detailsText(undefined)).toBe('');
	});
	it('prints the details on one line', () => {
		expect(detailsText({ deleted: 3, before: '2026-01-01T00:00:00Z' })).toBe(
			'{"deleted":3,"before":"2026-01-01T00:00:00Z"}'
		);
	});
});
