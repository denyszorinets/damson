import { expect, test } from '@playwright/test';

// Runs against damson dev (controller + one agent, SQLite); see playwright.config.ts.

async function makeJob(request: import('@playwright/test').APIRequestContext) {
	const name = `editor-e2e-${Date.now()}`;
	const created = await request.post('/api/v1/jobs', {
		data: {
			spec: {
				apiVersion: 'damson.dev/v1',
				kind: 'Job',
				metadata: { project: 'default', name },
				spec: {
					steps: [
						{ id: 'only', plugin: 'ssh', with: { node: 'n', command: 'echo one' } },
						{
							id: 'grp',
							type: 'group',
							steps: [{ id: 'inner', plugin: 'ssh', with: { node: 'n' } }]
						}
					]
				}
			}
		}
	});
	expect(created.status()).toBe(201);
	return (await created.json()).job.id as string;
}

test('a step is edited on the Steps tab and saved as a new version', async ({ page }) => {
	const id = await makeJob(page.request);
	await page.goto(`/jobs/${id}/edit`);
	await page.getByTestId('spec-tab-steps').click();
	await page.getByTestId('step-with-0').fill('{"node": "n", "command": "echo edited"}');
	await page.getByTestId('step-with-0').blur();
	// the group is kept, and shown whole
	await expect(page.getByTestId('step-whole-1')).toHaveValue(/inner/);
	await page.getByTestId('spec-tab-json').click();
	await expect(page.getByTestId('spec-text')).toHaveValue(/echo edited/);
	await expect(page.getByTestId('spec-text')).toHaveValue(/"inner"/);
	await page.getByTestId('spec-save').click();
	await expect(page).toHaveURL(new RegExp(`/projects/default/-/jobs/${id}$`));
	await page.getByTestId('job-tab-definition').click();
	await expect(page.getByTestId('version-row')).toHaveCount(2);
});

test('a step with a bad id shows its error count on the Steps tab', async ({ page }) => {
	const id = await makeJob(page.request);
	await page.goto(`/jobs/${id}/edit`);
	await page.getByTestId('spec-tab-steps').click();
	await page.getByTestId('step-id-0').fill('Bad_ID');
	await page.getByTestId('spec-validate').click();
	await expect(page.getByTestId('spec-tab-errors-steps')).toBeVisible();
	await expect(page.getByTestId('spec-issue').first()).toContainText('spec.steps[0]');
	// the text is untouched by the failed check
	await page.getByTestId('spec-tab-json').click();
	await expect(page.getByTestId('spec-text')).toHaveValue(/Bad_ID/);
});

test('the Details tab renames the job and Options adds an input', async ({ page }) => {
	const id = await makeJob(page.request);
	await page.goto(`/jobs/${id}/edit`);
	await page.getByTestId('detail-description').fill('hello');
	await page.getByTestId('spec-tab-options').click();
	await page.getByTestId('option-add').click();
	await expect(page.getByTestId('option-row')).toHaveCount(1);
	await page.getByTestId('spec-tab-json').click();
	await expect(page.getByTestId('spec-text')).toHaveValue(/"description": "hello"/);
	await expect(page.getByTestId('spec-text')).toHaveValue(/"option1"/);
});
