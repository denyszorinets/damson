import { expect, test } from '@playwright/test';

// The run dialog runs against damson dev. The admin operations (schedules,
// webhooks, service accounts) are mocked: the dev controller has no admin
// token, so these tests are about what the pages send and show.

const spec = {
	apiVersion: 'damson.dev/v1',
	kind: 'Job',
	metadata: { project: 'default', name: 'dialog-e2e' },
	spec: {
		options: {
			type: 'object',
			properties: {
				env: { type: 'string', title: 'Environment', enum: ['dev', 'prod'], default: 'dev' },
				replicas: { type: 'integer', title: 'Replicas', default: 2 }
			}
		},
		steps: [{ id: 'a', plugin: 'ssh', with: { node: 'n', command: 'echo hi' } }]
	}
};

test('run dialog is generated from spec.options and starts a run', async ({ page, request }) => {
	const created = await request.post('/api/v1/jobs', { data: { spec } });
	expect(created.status()).toBe(201);
	const { job } = await created.json();

	await page.goto(`/jobs/${job.id}`);
	await page.getByTestId('run-job').click();
	await expect(page.getByTestId('run-dialog')).toBeVisible();
	await page.getByLabel('Environment').selectOption('prod');
	await page.getByLabel('Replicas').fill('5');
	await page.getByRole('button', { name: 'Submit' }).click();

	await expect(page).toHaveURL(/\/-\/runs\/[0-9a-f-]+$/);
	const id = page.url().split('/').pop();
	const run = await (await request.get(`/api/v1/runs/${id}`)).json();
	expect(run.options).toEqual({ env: 'prod', replicas: 5 });
	expect(run.trigger.type).toBe('api');
});

test('run dialog for a job without options starts the run directly', async ({ page, request }) => {
	const created = await request.post('/api/v1/jobs', {
		data: {
			spec: {
				...spec,
				metadata: { project: 'default', name: 'dialog-none' },
				spec: { steps: spec.spec.steps }
			}
		}
	});
	const { job } = await created.json();
	await page.goto(`/jobs/${job.id}`);
	await page.getByTestId('run-job').click();
	await expect(page.getByText('This job takes no options.')).toBeVisible();
	await page.getByTestId('run-confirm').click();
	await expect(page).toHaveURL(/\/-\/runs\//);
});

test('job triggers: schedules and webhooks, webhook path shown once', async ({ page, request }) => {
	const created = await request.post('/api/v1/jobs', {
		data: { spec: { ...spec, metadata: { project: 'default', name: 'triggers-e2e' } } }
	});
	const { job } = await created.json();

	let schedules: Record<string, unknown>[] = [];
	let hooks: Record<string, unknown>[] = [];
	const auth: (string | undefined)[] = [];
	await page.route('**/api/v1/schedules**', async (r) => {
		const req = r.request();
		auth.push(req.headers()['authorization']);
		if (req.headers()['authorization'] !== 'Bearer dma_good') {
			return r.fulfill({
				status: 401,
				contentType: 'application/problem+json',
				json: { type: 'x', title: 'Unauthorized', status: 401 }
			});
		}
		if (req.method() === 'POST') {
			const body = req.postDataJSON();
			const s = {
				id: 's1',
				job_id: body.job_id,
				name: body.name,
				cron: body.cron,
				timezone: body.timezone,
				options: {},
				enabled: true,
				misfire: 'run_once',
				next_fire_at: '2026-01-02T03:00:00Z',
				version: 1,
				created_at: '2026-01-01T00:00:00Z',
				updated_at: '2026-01-01T00:00:00Z'
			};
			schedules = [s];
			return r.fulfill({ status: 201, json: s });
		}
		if (req.method() === 'DELETE') {
			schedules = [];
			return r.fulfill({ status: 204 });
		}
		return r.fulfill({ json: { items: schedules } });
	});
	await page.route('**/api/v1/webhooks**', async (r) => {
		const req = r.request();
		if (req.method() === 'POST') {
			const body = req.postDataJSON();
			const w = {
				id: 'w1',
				job_id: body.job_id,
				name: body.name,
				options: {},
				revoked: false,
				version: 1,
				created_at: '2026-01-01T00:00:00Z',
				updated_at: '2026-01-01T00:00:00Z'
			};
			hooks = [w];
			return r.fulfill({
				status: 201,
				json: { ...w, token: 'SECRETTOKEN', path: '/hooks/SECRETTOKEN' }
			});
		}
		if (req.method() === 'DELETE') {
			hooks = [{ ...hooks[0], revoked: true }];
			return r.fulfill({ status: 204 });
		}
		return r.fulfill({ json: { items: hooks } });
	});

	await page.goto(`/jobs/${job.id}`);
	await page.getByTestId('job-tab-schedule').click();
	await page.getByLabel('Admin token').fill('dma_bad');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('triggers-error')).toContainText('rejected');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();

	await page.getByLabel('Name', { exact: true }).first().fill('nightly');
	await page.getByRole('button', { name: 'Add schedule' }).click();
	await expect(page.getByTestId('schedule-row')).toContainText('nightly');
	await page.getByRole('button', { name: 'Delete' }).click();
	await expect(page.getByTestId('schedule-row')).toHaveCount(0);

	await page.getByTestId('job-tab-webhooks').click();
	await page.locator('#hook-name').fill('ci');
	await page.getByRole('button', { name: 'Add webhook' }).click();
	await expect(page.getByTestId('new-webhook-path')).toHaveText('/hooks/SECRETTOKEN');
	await page.getByRole('button', { name: 'Done' }).click();
	await expect(page.getByTestId('new-webhook')).toHaveCount(0);
	await expect(page.getByTestId('webhook-row')).toContainText('ci');
	await expect(page.locator('body')).not.toContainText('SECRETTOKEN');
	await page.getByRole('button', { name: 'Revoke' }).click();
	await expect(page.getByTestId('webhook-row')).toContainText('(revoked)');
});

test('service accounts page: create, token secret shown once, revoke', async ({ page }) => {
	let accounts: Record<string, unknown>[] = [];
	let toks: Record<string, unknown>[] = [];
	await page.route('**/api/v1/service-accounts**', async (r) => {
		const req = r.request();
		const url = new URL(req.url());
		if (req.headers()['authorization'] !== 'Bearer dma_good') {
			return r.fulfill({
				status: 401,
				contentType: 'application/problem+json',
				json: { type: 'x', title: 'Unauthorized', status: 401 }
			});
		}
		if (url.pathname.endsWith('/tokens')) {
			if (req.method() === 'POST') {
				const b = req.postDataJSON();
				const t = {
					id: 't1',
					service_account_id: 'a1',
					name: b.name,
					project: b.project,
					role: b.role,
					revoked: false,
					created_at: '2026-01-01T00:00:00Z'
				};
				toks = [t];
				return r.fulfill({ status: 201, json: { ...t, token: 'dms_SECRETVALUE' } });
			}
			return r.fulfill({ json: { items: toks } });
		}
		if (req.method() === 'POST') {
			accounts = [{ id: 'a1', name: req.postDataJSON().name, created_at: '2026-01-01T00:00:00Z' }];
			return r.fulfill({ status: 201, json: accounts[0] });
		}
		return r.fulfill({ json: { items: accounts } });
	});
	await page.route('**/api/v1/tokens/t1', (r) => {
		toks = [{ ...toks[0], revoked: true }];
		return r.fulfill({ status: 204 });
	});

	await page.goto('/service-accounts');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await page.locator('#acc-name').fill('ci');
	await page.getByRole('button', { name: 'Create account' }).click();
	await page.getByRole('button', { name: 'ci' }).click();
	await page.locator('#tok-name').fill('pipeline');
	await page.getByRole('button', { name: 'Create token' }).click();
	await expect(page.getByTestId('new-token-secret')).toHaveText('dms_SECRETVALUE');
	await page.getByRole('button', { name: 'Done' }).click();
	await expect(page.locator('body')).not.toContainText('dms_SECRETVALUE');
	await expect(page.getByTestId('token-row')).toContainText('runner');
	await page.getByRole('button', { name: 'Revoke' }).click();
	await expect(page.getByTestId('token-row')).toContainText('(revoked)');
});
