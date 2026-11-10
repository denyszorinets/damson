import { expect, test } from '@playwright/test';

test('home page renders the placeholder without console errors', async ({ page }) => {
	const errors: string[] = [];
	page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
	await page.goto('/');
	await expect(page.getByRole('heading', { name: 'Damson' })).toBeVisible();
	expect(errors).toEqual([]);
});

test('unknown routes fall back to the SPA shell', async ({ page }) => {
	await page.goto('/some/client/route');
	// The shell loads; the SvelteKit error page is rendered client-side.
	await expect(page.locator('body')).toContainText('404');
});
