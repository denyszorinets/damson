import { expect, test } from '@playwright/test';

// The nodes API is mocked: the page is what is under test (the controller's
// admin token is not available to the browser tests).

const base = {
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

test('nodes page: token, create, edit with if_version, force for a synced node, delete', async ({
	page
}) => {
	const nodes: Record<string, unknown>[] = [
		{ ...base, id: 'a', name: 'manual-1', address: '10.0.0.1', source: 'manual', version: 1 },
		{
			...base,
			id: 'b',
			name: 'synced-1',
			address: '10.0.0.2',
			source: 'sync1',
			external_id: 'x',
			version: 4
		}
	];
	const seen: { method: string; url: string; body: Record<string, unknown> | null }[] = [];

	await page.route('**/api/v1/nodes**', async (r) => {
		const req = r.request();
		const body = req.postData() ? JSON.parse(req.postData()!) : null;
		seen.push({ method: req.method(), url: req.url(), body });
		if (req.headers()['authorization'] !== 'Bearer dma_good') {
			return r.fulfill({
				status: 401,
				contentType: 'application/problem+json',
				json: { title: 'Unauthorized', status: 401 }
			});
		}
		const name = new URL(req.url()).pathname.split('/')[4];
		const i = nodes.findIndex((n) => n.name === name);
		if (req.method() === 'POST') {
			const n = { ...base, id: 'c', source: 'manual', version: 1, ...body, tags: body!.tags };
			nodes.push(n);
			return r.fulfill({ status: 201, json: n });
		}
		if (req.method() === 'PATCH') {
			nodes[i] = { ...nodes[i], ...body, version: (nodes[i].version as number) + 1 };
			return r.fulfill({ json: nodes[i] });
		}
		if (req.method() === 'DELETE') {
			nodes.splice(i, 1);
			return r.fulfill({ status: 204 });
		}
		if (name) return r.fulfill({ json: nodes[i] });
		return r.fulfill({ json: { items: nodes } });
	});

	await page.goto('/nodes');
	await page.getByLabel('Admin token').fill('dma_bad');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('nodes-error')).toContainText('rejected');
	await page.getByLabel('Admin token').fill('dma_good');
	await page.getByRole('button', { name: 'Use token' }).click();
	await expect(page.getByTestId('node-row')).toHaveCount(2);

	// Create.
	await page.getByTestId('new-node').click();
	await page.getByLabel('Name').fill('web-9');
	await page.getByLabel('Address').fill('10.0.0.9');
	await page.getByLabel('Tags (comma separated)').fill('prod, db');
	await page.getByTestId('save-node').click();
	await expect(page.getByTestId('node-row')).toHaveCount(3);
	const post = seen.find((s) => s.method === 'POST');
	expect(post?.body).toMatchObject({
		name: 'web-9',
		address: '10.0.0.9',
		tags: ['prod', 'db'],
		transport: 'ssh',
		host_key_policy: 'strict'
	});

	// Edit a manual node: if_version is the version the page read.
	await page.getByRole('link', { name: 'manual-1' }).click();
	await expect(page.getByTestId('node-details')).toContainText('10.0.0.1');
	await page.getByTestId('edit-node').click();
	await page.getByLabel('Address').fill('10.0.0.11');
	await page.getByTestId('save-node').click();
	await expect(page.getByTestId('node-details')).toContainText('10.0.0.11');
	const patch = seen.find((s) => s.method === 'PATCH');
	expect(patch?.body).toMatchObject({ if_version: 1, address: '10.0.0.11' });
	expect(patch?.url).not.toContain('force=true');

	// A synced node needs the force box before it can be saved or deleted.
	await page.goto('/nodes/synced-1');
	await page.getByTestId('edit-node').click();
	await expect(page.getByTestId('save-node')).toBeDisabled();
	await page.getByTestId('force').check();
	await page.getByTestId('save-node').click();
	await expect(page.getByTestId('node-details')).toContainText('version 5');
	expect(seen.filter((s) => s.method === 'PATCH').at(-1)?.url).toContain('force=true');

	await page.getByTestId('delete-node').click();
	await expect(page.getByTestId('confirm-delete')).toBeDisabled();
	await page.getByTestId('force-delete').check();
	await page.getByTestId('confirm-delete').click();
	await expect(page).toHaveURL(/\/nodes$/);
	await expect(page.getByTestId('node-row')).toHaveCount(2);
});
