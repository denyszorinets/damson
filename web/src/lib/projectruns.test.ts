import { describe, expect, it } from 'vitest';
import type { RunSummary } from '$lib/runs';
import { successRate } from './projectruns';

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
