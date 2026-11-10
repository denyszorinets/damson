import { expect, test } from '@playwright/test';

// The inventory and targets APIs are mocked: the pages are under test.

const node = (name: string) => ({
	id: `id-${name}`,
	name,
	address: `${name}.example.com`,
	port: 22,
	transport: 'ssh',
	os: 'linux',
	pool: '',
	tags: [],
	attributes: {},
	credential: '',
	host_key: '',
	host_key_observed: '',
	host_key_policy: 'strict',
	source: 'manual',
	external_id: '',
	enabled: true,
	version: 1,
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-01T00:00:00Z'
});

test('the nodes page previews a selector after the input settles', async ({ page }) => {
	const selectors: string[] = [];
	await page.route('**/api/v1/nodes**', (r) => {
		const sel = new URL(r.request().url()).searchParams.get('selector') ?? '';
		selectors.push(sel);
		return r.fulfill({ json: { items: sel ? [node('web-1')] : [node('web-1'), node('db-1')] } });
	});
	await page.addInitScript(() => sessionStorage.setItem('damson.admin_token', 'dma_good'));
	await page.goto('/nodes');
	await expect(page.getByTestId('node-row')).toHaveCount(2);
	await page.getByTestId('filter-selector').fill('node.name.startsWith("web")');
	await expect(page.getByTestId('node-row')).toHaveCount(1);
	// One request for the settled text, not one per keystroke.
	expect(selectors.filter((s) => s !== '')).toEqual(['node.name.startsWith("web")']);
});

test('a run page lists the targets it froze', async ({ page }) => {
	await page.route('**/api/v1/runs/*/targets**', (r) => {
		const cursor = new URL(r.request().url()).searchParams.get('cursor');
		const t = (name: string) => ({
			name,
			node_id: `id-${name}`,
			address: `${name}.example.com`,
			os: 'linux',
			pool: 'p',
			transport: 'ssh',
			tags: ['prod'],
			attributes: {}
		});
		return r.fulfill({
			json: cursor ? { items: [t('b')] } : { items: [t('a')], next_cursor: 'Mg' }
		});
	});
	const created = await page.request.post('/api/v1/jobs', {
		data: {
			spec: {
				apiVersion: 'damson.dev/v1',
				kind: 'Job',
				metadata: { project: 'default', name: 'targets-e2e' },
				spec: { steps: [{ id: 's', plugin: 'ssh', with: { node: 'n', command: 'true' } }] }
			}
		}
	});
	const { job } = await created.json();
	const run = await (await page.request.post(`/api/v1/jobs/${job.id}/runs`, { data: {} })).json();
	await page.addInitScript(() => sessionStorage.setItem('damson.admin_token', 'dma_good'));
	await page.goto(`/runs/${run.id}`);
	await page.getByTestId('targets-toggle').click();
	await expect(page.getByTestId('target-row')).toHaveCount(1);
	await page.getByTestId('targets-more').click();
	await expect(page.getByTestId('target-row')).toHaveCount(2);
});

test('a node page forgets the observed host key', async ({ page }) => {
	let observed = 'ssh-ed25519 AAAA';
	const calls: string[] = [];
	await page.route('**/api/v1/nodes/tofu-1**', (r) => {
		const req = r.request();
		calls.push(`${req.method()} ${new URL(req.url()).pathname}`);
		if (req.method() === 'DELETE') {
			observed = '';
			return r.fulfill({ status: 204 });
		}
		return r.fulfill({
			json: {
				...node('tofu-1'),
				host_key_policy: 'tofu',
				host_key_observed: observed,
				host_key_observed_fingerprint: observed ? 'SHA256:abc' : ''
			}
		});
	});
	await page.addInitScript(() => sessionStorage.setItem('damson.admin_token', 'dma_good'));
	await page.goto('/nodes/tofu-1');
	await expect(page.getByTestId('node-details')).toContainText('SHA256:abc');
	await page.getByTestId('forget-host-key').click();
	await expect(page.getByTestId('forget-host-key')).toHaveCount(0);
	expect(calls).toContain('DELETE /api/v1/nodes/tofu-1/host-key');
});
