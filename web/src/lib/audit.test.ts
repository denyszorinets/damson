import { describe, expect, it } from 'vitest';
import { actorLabel, auditQuery, detailsText, distinct, emptyFilter, utcTime } from './audit';

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

describe('auditQuery', () => {
	it('sends only what is set; system rows are hidden by default', () => {
		expect(auditQuery(emptyFilter())).toEqual({ limit: 50, hide_system: true });
	});
	it('maps every field', () => {
		const q = auditQuery(
			{
				targetType: ' job ',
				target: 'j1',
				project: 'acme',
				action: 'job.create',
				actor: 'user:u1',
				from: '2026-01-02T03:04',
				to: '2026-01-03T00:00:30',
				hideSystem: false
			},
			'c2'
		);
		expect(q).toEqual({
			limit: 50,
			target_type: 'job',
			target: 'j1',
			project: 'acme',
			action: 'job.create',
			actor: 'user:u1',
			from: '2026-01-02T03:04:00Z',
			to: '2026-01-03T00:00:30Z',
			cursor: 'c2'
		});
	});
});

describe('utcTime', () => {
	it('reads a local value as UTC and drops anything else', () => {
		expect(utcTime('2026-01-02T03:04')).toBe('2026-01-02T03:04:00Z');
		expect(utcTime('')).toBe('');
		expect(utcTime('yesterday')).toBe('');
	});
});

describe('actorLabel and distinct', () => {
	it('prefers the display name', () => {
		expect(actorLabel({ actor: 'user:u1', actor_name: 'u1@example.org' })).toBe('u1@example.org');
		expect(actorLabel({ actor: 'cli:ops' })).toBe('cli:ops');
	});
	it('lists each value once, sorted', () => {
		const ev = (actor: string, action: string) => ({ actor, action }) as never;
		const events = [ev('b', 'x'), ev('a', 'x'), ev('b', 'y')];
		expect(distinct(events, 'actor')).toEqual(['a', 'b']);
		expect(distinct(events, 'action')).toEqual(['x', 'y']);
	});
});
