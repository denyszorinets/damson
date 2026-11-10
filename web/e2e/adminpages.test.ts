import { expect, test } from '@playwright/test';

// The instance-admin pages with their APIs mocked (the admin token is not
// available to the browser tests): what each page shows and sends.

const node = {
	id: 'a',
	name: 'web-01',
	address: '10.0.0.1',
	source: 'manual',
	version: 1,
	tags: ['prod'],
	attributes: {},
	credential: '',
	host_key: '',
	host_key_observed: '',
	external_id: '',
	enabled: true,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z',
	port: 22,
	transport: 'ssh',
	os: 'linux',
	pool: '',
	host_key_policy: 'strict'
};

test('nodes page: the builder writes a selector, which can be saved, reused and deleted', async ({
	page
}) => {
	const selectors: (string | null)[] = [];
	await page.route('**/api/v1/nodes**', (r) => {
		selectors.push(new URL(r.request().url()).searchParams.get('selector'));
		return r.fulfill({ json: { items: [node] } });
	});
	await page.goto('/nodes');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('nodes-table')).toBeVisible();

	// Clauses are built from fields; the value is quoted so it cannot add to the expression.
	await page.getByTestId('builder-field').selectOption('os');
	await page.getByTestId('builder-value').fill('linux');
	await page.getByTestId('builder-add').click();
	await page.getByTestId('builder-field').selectOption('tag');
	await page.getByTestId('builder-value').fill("pr'od");
	await page.getByTestId('builder-add').click();
	const expected = "node.os == 'linux' && 'pr\\'od' in node.tags";
	await expect(page.getByTestId('filter-selector')).toHaveValue(expected);
	await expect.poll(() => selectors.at(-1)).toBe(expected);

	// An attribute needs a key that is an identifier.
	await page.getByTestId('builder-field').selectOption('attribute');
	await page.getByTestId('builder-key').fill('bad key');
	await page.getByTestId('builder-value').fill('eu');
	await page.getByTestId('builder-add').click();
	await expect(page.getByTestId('filter-selector')).toHaveValue(expected); // refused: unchanged

	// Save it, clear the filter, use the saved one, delete it.
	await page.getByTestId('save-name').fill('linux prod');
	await page.getByTestId('save-selector').click();
	await expect(page.getByTestId('use-saved-linux prod')).toBeVisible();
	await page.getByTestId('filter-selector').fill('');
	await page.getByTestId('use-saved-linux prod').click();
	await expect(page.getByTestId('filter-selector')).toHaveValue(expected);
	await page.reload(); // kept in this browser (the token stays in the tab)
	await expect(page.getByTestId('use-saved-linux prod')).toBeVisible();
	await page.getByRole('button', { name: 'Delete saved selector linux prod' }).click();
	await expect(page.getByTestId('saved-selectors')).toHaveCount(0);
});

test('agents page: health tells a connected agent that went quiet from a healthy one', async ({
	page
}) => {
	const agent = (id: string, status: string, lastSeen: string) => ({
		id,
		pool_id: 'p1',
		status,
		version: 'dev',
		protocol: 1,
		labels: {},
		capabilities: {},
		capacity: 4,
		connected_at: '2026-01-01T00:00:00Z',
		last_seen: lastSeen
	});
	const now = Date.now();
	await page.route('**/api/v1/pools', (r) =>
		r.fulfill({
			json: { items: [{ id: 'p1', name: 'default', created_at: '2026-01-01T00:00:00Z' }] }
		})
	);
	await page.route('**/api/v1/agents**', (r) =>
		r.fulfill({
			json: {
				items: [
					agent('fresh', 'connected', new Date(now).toISOString()),
					agent('quiet', 'connected', new Date(now - 5 * 60_000).toISOString()),
					agent('gone', 'disconnected', new Date(now - 60 * 60_000).toISOString())
				]
			}
		})
	);
	await page.goto('/agents');
	const health = (id: string) =>
		page.getByTestId('agent-row').filter({ hasText: id }).getByTestId('agent-health');
	await expect(health('fresh')).toHaveText('healthy');
	await expect(health('quiet')).toHaveText('stale');
	await expect(health('gone')).toHaveText('offline');
});

test('secrets page: copy reference puts the reference, never a value, on the clipboard', async ({
	page,
	context
}) => {
	await context.grantPermissions(['clipboard-read', 'clipboard-write']);
	await page.route('**/api/v1/secret-types', (r) =>
		r.fulfill({ json: { items: [{ name: 'generic' }] } })
	);
	await page.route('**/api/v1/secrets**', (r) =>
		r.fulfill({
			json: {
				items: [
					{
						ref: 'secret://team/db-password',
						type: 'generic',
						fields: ['value'],
						current_version: 3
					}
				]
			}
		})
	);
	await page.goto('/secrets');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('secret-row')).toHaveCount(1);
	await page.getByTestId('copy-ref').click();
	await expect(page.getByTestId('copy-ref')).toHaveText('Copied');
	expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
		'secret://team/db-password'
	);
});
