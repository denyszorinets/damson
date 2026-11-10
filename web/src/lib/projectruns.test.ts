import { describe, expect, it } from 'vitest';
import type { RunSummary } from '$lib/runs';
import type { Job } from '$lib/jobs';
import { jobHealth, successRate } from './projectruns';

const run = (status: string) => ({ status }) as RunSummary;

describe('successRate', () => {
	it.each([
		[[], null],
		[['running', 'pending'], null],
		[['canceled'], null],
		[['succeeded'], 100],
		[['failed'], 0],
		[['succeeded', 'failed', 'running', 'canceled'], 50],
		[['succeeded', 'succeeded', 'failed'], 67]
	])('%j', (statuses, want) => {
		expect(successRate(statuses.map(run))).toBe(want);
	});
});

describe('jobHealth', () => {
	const r = (job_id: string, status: string) => ({ job_id, status }) as RunSummary;
	const jobs = new Map([
		['a', { id: 'a' } as Job],
		['b', { id: 'b' } as Job],
		['c', { id: 'c' } as Job]
	]);

	it('reports the newest finished result and the rate per job, and keeps jobs with no runs', () => {
		const got = jobHealth(
			[r('a', 'running'), r('a', 'failed'), r('a', 'succeeded'), r('b', 'succeeded')],
			jobs
		);
		expect(got).toEqual([
			{ jobId: 'a', last: 'failed', rate: 50, runs: 3 },
			{ jobId: 'b', last: 'succeeded', rate: 100, runs: 1 },
			{ jobId: 'c', last: null, rate: null, runs: 0 }
		]);
	});

	it('lists a run whose job is not in the map', () => {
		expect(jobHealth([r('z', 'failed')], new Map())).toEqual([
			{ jobId: 'z', last: 'failed', rate: 0, runs: 1 }
		]);
	});
});
