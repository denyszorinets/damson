import { expect, test } from '@playwright/test';

// The controller under test has no KEK, so the secrets API is mocked: this test
// is about what the page sends and what it keeps.

const VALUE = 'ui-secret-value-123';

test('secrets page: token, create, values never kept, delete', async ({ page }) => {
	const types = {
		items: [
			{ name: 'generic' },
			{
				name: 'password',
				fields: [
					{ name: 'username', required: false, multiline: false },
					{ name: 'password', required: true, multiline: false }
				]
			}
		]
	};
	let list: Record<string, unknown>[] = [];
	const seen: { auth: string | undefined; url: string; body: string | null }[] = [];

	await page.route('**/api/v1/secret-types', (r) => r.fulfill({ json: types }));
	await page.route('**/api/v1/secrets**', async (r) => {
		const req = r.request();
		seen.push({ auth: req.headers()['authorization'], url: req.url(), body: req.postData() });
		if (req.headers()['authorization'] !== 'Bearer dma_good') {
			return r.fulfill({
				status: 401,
				contentType: 'application/problem+json',
				json: { type: 'urn:damson:problem:unauthorized', title: 'Unauthorized', status: 401 }
			});
		}
		if (req.method() === 'PUT') {
			const meta = {
				ref: 'secret://infra/billing/db',
				scope: 'infra/billing',
				name: 'db',
				type: 'password',
				fields: ['password'],
				current_version: 1,
				created_at: '2026-01-01T00:00:00Z',
				updated_at: '2026-01-01T00:00:00Z'
			};
			list = [meta];
			return r.fulfill({ status: 201, json: meta });
		}
		if (req.method() === 'DELETE') {
			list = [];
			return r.fulfill({ status: 204 });
		}
		return r.fulfill({ json: { items: list } });
	});

	await page.goto('/secrets');
	// A wrong token is rejected and asked for again.
	await page.getByLabel('Admin token').fill('dma_bad');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('secrets-error')).toContainText('rejected');
	await expect(page.getByTestId('token-form')).toBeVisible();

	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('secrets-table')).toBeVisible();

	await page.getByTestId('new-secret').click();
	await page.getByLabel('Scope').fill('infra/billing');
	await page.getByLabel('Name', { exact: true }).fill('db');
	await page.getByTestId('secret-type').click();
	await page.getByRole('option', { name: 'password' }).click();
	await page.getByTestId('field-password').fill(VALUE);
	await page.getByTestId('save-secret').click();

	const put = seen.find((s) => s.url.includes('/secrets/') && s.body);
	expect(put?.url).toContain('/secrets/infra%2Fbilling/db');
	expect(JSON.parse(put!.body!)).toMatchObject({ type: 'password', if_version: 0 });
	await expect(page.getByTestId('secret-row')).toContainText('secret://infra/billing/db');
	// The value is nowhere in the page after saving.
	expect(await page.content()).not.toContain(VALUE);
	await expect(page.getByTestId('secret-form')).toHaveCount(0);

	await page.getByRole('button', { name: 'Delete' }).click();
	await page.getByRole('button', { name: 'Confirm delete' }).click();
	await expect(page.getByTestId('secret-row')).toHaveCount(0);

	await page.getByTestId('forget-token').click();
	await expect(page.getByTestId('token-form')).toBeVisible();
});
