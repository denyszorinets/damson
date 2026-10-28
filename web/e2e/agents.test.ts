import { expect, test } from '@playwright/test';

// Runs against damson dev (controller + one agent, SQLite); see playwright.config.ts.

test('agents page shows the dev agent connected', async ({ page }) => {
	const errors: string[] = [];
	page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
	await page.goto('/');
	await page.getByRole('link', { name: 'Agents' }).click();
	await expect(page).toHaveURL(/\/agents$/);
	await expect(page.getByRole('heading', { name: 'Agents', level: 1 })).toBeVisible();

	const row = page.getByTestId('agent-row');
	await expect(row).toHaveCount(1);
	await expect(row.getByText('connected', { exact: true })).toBeVisible();
	await expect(row.getByText('os=linux')).toBeVisible();
	await expect(page.getByTestId('pools-table').getByText('default')).toBeVisible();
	expect(errors).toEqual([]);
});

test('agents page shows a problem+json error', async ({ page }) => {
	await page.route('**/api/v1/pools*', (route) =>
		route.fulfill({
			status: 500,
			contentType: 'application/problem+json',
			body: JSON.stringify({
				type: 'about:blank',
				title: 'Internal Server Error',
				status: 500,
				detail: 'boom'
			})
		})
	);
	await page.goto('/agents');
	await expect(page.getByTestId('agents-error')).toContainText('boom');
});
