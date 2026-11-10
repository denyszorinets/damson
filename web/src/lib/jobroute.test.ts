import { describe, expect, it } from 'vitest';
import { parseProjectRoute } from './projectroute';
import { jobRest, jobTab, runRest } from './jobroute';

describe('jobRest and runRest', () => {
	it('build the project-first URLs and parse back', () => {
		expect(jobRest('acme/pay', 'j1')).toBe('acme/pay/-/jobs/j1');
		expect(jobRest('acme/pay', 'j1', 'runs')).toBe('acme/pay/-/jobs/j1/runs');
		expect(runRest('acme', 'r1')).toBe('acme/-/runs/r1');
		expect(parseProjectRoute(jobRest('acme/pay', 'j1', 'webhooks'))).toEqual({
			path: 'acme/pay',
			tab: ['jobs', 'j1', 'webhooks']
		});
	});
});

describe('jobTab', () => {
	it.each([
		[[], ''],
		[['runs'], 'runs'],
		[['definition'], 'definition'],
		[['schedule'], 'schedule'],
		[['webhooks'], 'webhooks'],
		[['nope'], null],
		[['runs', 'x'], null]
	])('%j', (sub, want) => {
		expect(jobTab(sub)).toBe(want);
	});
});
