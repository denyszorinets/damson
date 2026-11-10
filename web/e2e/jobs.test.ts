import { expect, test } from '@playwright/test';

// Runs against damson dev (controller + one agent, SQLite); see playwright.config.ts.

const step = (id: string, extra: Record<string, unknown> = {}) => ({
	id,
	plugin: 'ssh',
	with: { node: 'n', command: `echo ${id}` },
	...extra
});

const spec = {
	apiVersion: 'damson.dev/v1',
	kind: 'Job',
	metadata: { project: 'default', name: 'graph-e2e' },
	spec: {
		steps: [
			step('prep', { when: '1 == 1' }),
			{
				id: 'deploy',
				type: 'group',
				for_each: '["a","b"]',
				max_parallel: 1,
				steps: [step('build', { retry: { max_attempts: 2 } })],
				catch: [step('rollback')],
				finally: [step('tidy')]
			},
			step('notify', { when: 'always()' })
		]
	}
};

test('job graph shows groups, catch, finally, for_each and edge kinds', async ({
	page,
	request
}) => {
	const errors: string[] = [];
	page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
	const created = await request.post('/api/v1/jobs', { data: { spec } });
	expect(created.status()).toBe(201);
	const { job } = await created.json();

	await page.goto('/jobs');
	await page.getByRole('link', { name: 'graph-e2e' }).click();
	await expect(page).toHaveURL(new RegExp(`/jobs/${job.id}$`));
	await expect(page.getByTestId('job-title')).toHaveText('graph-e2e');

	const node = (id: string) => page.getByTestId(`graph-node-${id}`);
	await expect(node('$start')).toHaveAttribute('data-kind', 'start');
	await expect(node('prep')).toHaveAttribute('data-kind', 'step');
	await expect(node('deploy')).toHaveAttribute('data-kind', 'group');
	await expect(node('deploy/for_each[*]/build')).toHaveAttribute('data-kind', 'step');
	await expect(node('deploy/for_each[*]/catch')).toHaveAttribute('data-kind', 'catch');
	await expect(node('deploy/for_each[*]/catch/rollback')).toBeVisible();
	await expect(node('deploy/for_each[*]/finally/tidy')).toBeVisible();
	await expect(node('$end')).toHaveAttribute('data-kind', 'end');
	await expect(page.getByTestId('graph-foreach-deploy')).toContainText('["a","b"]');
	await expect(node('prep')).toContainText('when: 1 == 1');
	await expect(node('deploy/for_each[*]/build')).toContainText('retry ×2');

	// Edge kinds come from the server's graph; the view only styles them.
	for (const kind of ['sequence', 'skip', 'catch', 'finally']) {
		await expect(page.locator(`.svelte-flow__edge.edge-${kind}`).first()).toBeAttached();
	}
	expect(errors).toEqual([]);
});

test('job graph shows a problem+json error', async ({ page }) => {
	await page.goto('/jobs/does-not-exist');
	await expect(page.getByTestId('job-error')).toContainText('Not Found');
});
