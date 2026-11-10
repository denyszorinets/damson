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

// A small in-memory audit API: the filters the page sends decide the rows.
test('audit page: actor filter and system rows hidden until toggled', async ({ page }) => {
	const rows = [
		{ id: '1', actor: 'user:u1', actor_name: 'ada@example.org', action: 'job.create' },
		{ id: '2', actor: 'user:u2', actor_name: 'bob@example.org', action: 'job.create' },
		{ id: '3', actor: 'system:dispatch', actor_name: '', action: 'secret.read' },
		{ id: '4', actor: 'user:u1', actor_name: 'ada@example.org', action: 'run.trigger' }
	].map((r) => ({
		...r,
		at: '2026-01-02T03:04:05Z',
		target_type: 'job',
		target: `t${r.id}`,
		details: {}
	}));
	const urls: string[] = [];
	await page.route('**/api/v1/audit**', (r) => {
		const u = new URL(r.request().url());
		urls.push(r.request().url());
		const q = u.searchParams;
		const items = rows.filter(
			(e) =>
				(!q.get('actor') || e.actor === q.get('actor')) &&
				(!q.get('action') || e.action === q.get('action')) &&
				(q.get('hide_system') !== 'true' || !e.actor.startsWith('system:')) &&
				(!q.get('from') || e.at >= q.get('from')!) &&
				(!q.get('to') || e.at < q.get('to')!)
		);
		return r.fulfill({ json: { items } });
	});

	await page.goto('/audit');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();

	// System rows are hidden on load; the readable actor is shown, the id is the tooltip.
	await expect(page.getByTestId('audit-row')).toHaveCount(3);
	expect(urls.at(-1)).toContain('hide_system=true');
	await expect(page.getByTestId('audit-table')).not.toContainText('system:dispatch');
	await expect(page.getByTestId('audit-row').first()).toContainText('ada@example.org');
	await expect(page.getByTitle('user:u1').first()).toBeVisible();

	await page.getByTestId('audit-system-toggle').click();
	await expect(page.getByTestId('audit-row')).toHaveCount(4);
	await expect(page.getByTestId('audit-table')).toContainText('system:dispatch');
	expect(urls.at(-1)).not.toContain('hide_system');

	// Filtering by actor returns only that actor.
	await page.getByLabel('Actor').fill('user:u2');
	await page.getByRole('button', { name: 'Filter' }).click();
	await expect(page.getByTestId('audit-row')).toHaveCount(1);
	await expect(page.getByTestId('audit-row')).toContainText('bob@example.org');
	expect(urls.at(-1)).toContain('actor=user%3Au2');

	// Action and time range reach the query; Clear restores the default view.
	await page.getByLabel('Actor').fill('');
	await page.getByLabel('Action').fill('run.trigger');
	await page.getByLabel('From (UTC)').fill('2026-01-02T03:00');
	await page.getByLabel('To (UTC)').fill('2026-01-02T04:00');
	await page.getByRole('button', { name: 'Filter' }).click();
	await expect(page.getByTestId('audit-row')).toHaveCount(1);
	expect(urls.at(-1)).toContain('action=run.trigger');
	expect(urls.at(-1)).toContain('from=2026-01-02T03%3A00%3A00Z');
	expect(urls.at(-1)).toContain('to=2026-01-02T04%3A00%3A00Z');

	await page.getByTestId('audit-clear').click();
	await expect(page.getByTestId('audit-row')).toHaveCount(3);
	expect(urls.at(-1)).toContain('hide_system=true');
});
