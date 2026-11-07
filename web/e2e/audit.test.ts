import { expect, test } from '@playwright/test';

// The audit API is mocked: this test is about the token flow, the filter query
// and paging.

const ev = (id: string, action: string, target: string, details: unknown) => ({
	id,
	at: '2026-01-02T03:04:05.123456Z',
	actor: 'cli:ops',
	action,
	target_type: 'secret',
	target,
	details
});

test('audit page: token, rows, filter, load more', async ({ page }) => {
	const urls: string[] = [];
	await page.route('**/api/v1/audit**', (r) => {
		const req = r.request();
		urls.push(req.url());
		if (req.headers()['authorization'] !== 'Bearer dma_good') {
			return r.fulfill({
				status: 401,
				contentType: 'application/problem+json',
				json: { type: 'urn:damson:problem:unauthorized', title: 'Unauthorized', status: 401 }
			});
		}
		const u = new URL(req.url());
		if (u.searchParams.get('cursor') === 'c2') {
			return r.fulfill({ json: { items: [ev('3', 'secret.create', 'a/old', {})] } });
		}
		return r.fulfill({
			json: {
				items: [
					ev('1', 'secret.update', 'a/db', { version: 2, fields: ['password'] }),
					ev('2', 'admin_token.create', 'admin_token', {})
				],
				next_cursor: 'c2'
			}
		});
	});

	await page.goto('/audit');
	await page.getByLabel('Admin token').fill('dma_bad');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('audit-error')).toContainText('rejected');

	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('audit-row')).toHaveCount(2);
	await expect(page.getByTestId('audit-row').first()).toContainText('secret.update');
	await expect(page.getByTestId('audit-row').first()).toContainText('2026-01-02 03:04:05');
	await expect(page.getByTestId('audit-row').first()).toContainText('"fields":["password"]');

	await page.getByTestId('audit-more').click();
	await expect(page.getByTestId('audit-row')).toHaveCount(3);
	await expect(page.getByTestId('audit-more')).toHaveCount(0);

	await page.getByLabel('Target type').fill('secret');
	await page.getByLabel('Target', { exact: true }).fill('a/db');
	await page.getByRole('button', { name: 'Filter' }).click();
	await expect.poll(() => urls.at(-1)).toContain('target_type=secret');
	expect(urls.at(-1)).toContain('target=a%2Fdb');
});
