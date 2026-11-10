import { expect, test, type Page } from '@playwright/test';

// Runs against damson dev (see playwright.config.ts). The run dialog is a form
// generated from the job's options schema; the server's findings go beside fields.

const spec = {
	apiVersion: 'damson.dev/v1',
	kind: 'Job',
	metadata: { project: 'default', name: 'dialog-e2e' },
	spec: {
		options: {
			type: 'object',
			required: ['env'],
			properties: {
				env: {
					type: 'string',
					enum: ['staging', 'prod'],
					default: 'staging',
					title: 'Environment'
				},
				version: { type: 'string', pattern: '^[0-9.]+$', default: '1.0.0', title: 'Version' }
			}
		},
		steps: [{ id: 's', plugin: 'ssh', with: { node: 'n', command: 'echo hi' } }]
	}
};

async function openDialog(page: Page): Promise<string> {
	const created = await page.request.post('/api/v1/jobs', { data: { spec } });
	const id = created.status() === 201 ? (await created.json()).job.id : undefined;
	let jobId = id;
	if (!jobId) {
		const list = await (await page.request.get('/api/v1/jobs?limit=200')).json();
		jobId = list.items.find((j: { name: string }) => j.name === 'dialog-e2e').id;
	}
	await page.goto(`/jobs/${jobId}`);
	await page.getByTestId('run-job').click();
	await expect(page.getByTestId('schema-form')).toBeVisible();
	return jobId as string;
}

test('a value the form can check is refused beside its field and no run is requested', async ({
	page
}) => {
	let posts = 0;
	await page.route('**/api/v1/jobs/*/runs', (route) => {
		if (route.request().method() === 'POST') posts++;
		return route.continue();
	});
	await openDialog(page);
	await page.getByLabel('Version').fill('not a version');
	await page.getByRole('button', { name: /submit/i }).click();
	await expect(page.getByTestId('schema-form')).toContainText('must match pattern');
	await expect(page).toHaveURL(/\/jobs\/[^/]+$/);
	expect(posts).toBe(0);
});

test('an error the server finds is shown beside the field and no run is created', async ({
	page
}) => {
	const jobId = await openDialog(page);
	const before = await (await page.request.get(`/api/v1/runs?job=${jobId}`)).json();
	await page.route('**/api/v1/jobs/*/runs', (route) => {
		if (route.request().method() !== 'POST') return route.continue();
		return route.fulfill({
			status: 422,
			contentType: 'application/problem+json',
			body: JSON.stringify({
				type: 'about:blank',
				title: 'Unprocessable Entity',
				status: 422,
				detail: 'the options are invalid',
				errors: [{ message: 'version 9.9 is not published', location: 'options.version' }]
			})
		});
	});
	await page.getByRole('button', { name: /submit/i }).click();
	await expect(page.getByTestId('schema-form')).toContainText('version 9.9 is not published');
	await expect(page.getByTestId('run-error')).toHaveCount(0); // beside the field, not a banner
	await expect(page).toHaveURL(/\/jobs\/[^/]+$/);
	const after = await (await page.request.get(`/api/v1/runs?job=${jobId}`)).json();
	expect((after.items ?? []).length).toBe((before.items ?? []).length);
});

test('a valid form starts a run and opens it', async ({ page }) => {
	await openDialog(page);
	await page.getByRole('button', { name: /submit/i }).click();
	await expect(page).toHaveURL(/\/runs\/[^/]+$/);
});
