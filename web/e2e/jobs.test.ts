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

test('a job that does not exist shows the not-found page', async ({ page }) => {
	await page.goto('/jobs/does-not-exist');
	await expect(page.getByTestId('not-found')).toContainText('This job was not found');
});

test('a job lists its versions, compares two, and a run names the version it ran', async ({
	page
}) => {
	const name = `versions-e2e-${Date.now()}`;
	const spec = (command: string) => ({
		apiVersion: 'damson.dev/v1',
		kind: 'Job',
		metadata: { project: 'default', name },
		spec: { steps: [{ id: 'only', plugin: 'ssh', with: { node: 'n', command } }] }
	});
	const created = await page.request.post('/api/v1/jobs', { data: { spec: spec('echo one') } });
	expect(created.status()).toBe(201);
	const { job } = await created.json();
	const v2 = await page.request.post(`/api/v1/jobs/${job.id}/versions`, {
		data: { spec: spec('echo two'), expected_version: 1 }
	});
	expect(v2.status()).toBe(201);

	await page.goto(`/jobs/${job.id}`);
	await expect(page.getByTestId('version-row')).toHaveCount(2);
	await expect(page.locator('[data-testid="version-row"][data-version="2"]')).toContainText(
		'current'
	);
	// The default comparison is the previous version with the current one.
	await page.getByTestId('diff-show').click();
	await expect(page.getByTestId('diff')).toBeVisible();
	await expect(page.locator('[data-testid="diff-line"][data-kind="removed"]')).toHaveCount(1);
	await expect(page.locator('[data-testid="diff-line"][data-kind="removed"]')).toContainText(
		'echo one'
	);
	await expect(page.locator('[data-testid="diff-line"][data-kind="added"]')).toContainText(
		'echo two'
	);
	// A run of version 1 and a run of version 2 each name theirs.
	const run1 = await page.request.post(`/api/v1/jobs/${job.id}/runs`, {
		data: { version: 1 }
	});
	expect(run1.status()).toBe(201);
	await page.goto(`/runs/${(await run1.json()).id}`);
	await expect(page.getByTestId('run-version')).toHaveText('job version 1');
	const run2 = await page.request.post(`/api/v1/jobs/${job.id}/runs`, { data: {} });
	await page.goto(`/runs/${(await run2.json()).id}`);
	await expect(page.getByTestId('run-version')).toHaveText('job version 2');
});
