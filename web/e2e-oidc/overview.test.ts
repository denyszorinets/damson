import { expect, test, type Browser, type Page } from '@playwright/test';

// The project Overview as three users (see playwright.oidc.config.ts). The "ov-"
// prefix keeps the expectations exact whatever other tests created.
const idp = 'http://127.0.0.1:4191';

async function as(browser: Browser, sub: string): Promise<Page> {
	const ctx = await browser.newContext();
	expect((await ctx.request.post(`${idp}/_login-as?sub=${sub}`)).status()).toBe(204);
	const page = await ctx.newPage();
	await page.goto('/');
	await expect(page.getByTestId('user-name')).toBeVisible();
	return page;
}

test.describe.configure({ mode: 'serial' });

test.beforeAll(async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	const me = await (await page.request.get('/api/v1/me')).json();
	const h = { 'X-CSRF-Token': me.csrf_token };
	const ok = (r: { status(): number }) => expect([200, 201, 409]).toContain(r.status());
	for (const path of ['ov-acme', 'ov-acme/pay'])
		ok(await page.request.post('/api/v1/projects', { data: { path }, headers: h }));
	ok(
		await page.request.put('/api/v1/projects/ov-acme/access-rules', {
			data: { rules: [{ role: 'runner', rule: "'team-ops' in claims.groups" }] },
			headers: h
		})
	);
	const spec = {
		apiVersion: 'damson.dev/v1',
		kind: 'Job',
		metadata: { project: 'ov-acme/pay', name: 'charge' },
		spec: { steps: [{ id: 's', plugin: 'ssh', with: { node: 'n', command: 'echo hi' } }] }
	};
	const created = await page.request.post('/api/v1/jobs', { data: { spec }, headers: h });
	ok(created);
	if (created.status() === 201) {
		const { job } = await created.json();
		const run = await page.request.post(`/api/v1/jobs/${job.id}/runs`, {
			data: {},
			headers: h
		});
		expect(run.status()).toBe(201);
	}
});

test('the Overview shows running, waiting, the job health and recent runs of the subtree', async ({
	browser
}) => {
	const bob = await as(browser, 'bob-sub');
	await bob.goto('/projects/ov-acme/-/overview');
	await expect(bob.getByTestId('overview')).toBeVisible();
	await expect(bob.getByTestId('running-now')).toBeVisible();
	await expect(bob.getByTestId('waiting')).toBeVisible();
	await expect(bob.getByTestId('job-health-row')).toHaveCount(1);
	await expect(bob.getByTestId('job-health-row')).toContainText('charge');
	await expect(bob.getByTestId('job-health-last')).toHaveText(/failed|succeeded|in progress/);
	// The run is on the child project; its row links to it.
	await expect(bob.getByTestId('run-row').first().getByRole('link')).toHaveAttribute(
		'href',
		/\/projects\/ov-acme\/pay\/.*runs/
	);
});

test('a user with no role on the project gets not-found, not an Overview', async ({ browser }) => {
	const erin = await as(browser, 'erin-sub');
	await erin.goto('/projects/ov-acme/-/overview');
	await expect(erin.getByTestId('overview')).toHaveCount(0);
	await expect(erin.locator('body')).toContainText(/not found|404/i);
});
