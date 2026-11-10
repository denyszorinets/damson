import { expect, test, type Browser, type Page } from '@playwright/test';

// The sidebar tree as five users (see playwright.oidc.config.ts). Names carry a
// "tree-" prefix so the expectations are exact whatever other tests created.
const idp = 'http://127.0.0.1:4191';

async function as(browser: Browser, sub: string): Promise<Page> {
	const ctx = await browser.newContext();
	expect((await ctx.request.post(`${idp}/_login-as?sub=${sub}`)).status()).toBe(204);
	const page = await ctx.newPage();
	await page.goto('/');
	await expect(page.getByTestId('user-name')).toBeVisible(); // logged in, session cookie set
	return page;
}

const step = { id: 's', plugin: 'ssh', with: { node: 'n', command: 'echo hi' } };
const job = (project: string, name: string) => ({
	apiVersion: 'damson.dev/v1',
	kind: 'Job',
	metadata: { project, name },
	spec: { steps: [step] }
});

test.describe.configure({ mode: 'serial' });

test.beforeAll(async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	const me = await (await page.request.get('/api/v1/me')).json();
	const h = { 'X-CSRF-Token': me.csrf_token };
	const ok = (r: { status(): number }) => expect([200, 201, 409]).toContain(r.status());
	for (const path of ['tree-acme', 'tree-acme/pay', 'tree-ops'])
		ok(await page.request.post('/api/v1/projects', { data: { path }, headers: h }));
	const rules = (path: string, rules: { role: string; rule: string }[]) =>
		page.request.put(`/api/v1/projects/${encodeURIComponent(path)}/access-rules`, {
			data: { rules },
			headers: h
		});
	ok(
		await rules('tree-acme', [
			{ role: 'runner', rule: "'team-ops' in claims.groups" },
			{ role: 'viewer', rule: "'team-viewers' in claims.groups" }
		])
	);
	ok(await rules('tree-acme/pay', [{ role: 'editor', rule: "'team-leads' in claims.groups" }]));
	const ids: Record<string, string> = {};
	for (const [project, name] of [
		['tree-acme', 'deploy'],
		['tree-acme/pay', 'charge'],
		['tree-ops', 'nightly']
	]) {
		const r = await page.request.post('/api/v1/jobs', {
			data: { spec: job(project, name) },
			headers: h
		});
		ok(r);
		if (r.status() === 201) ids[name] = (await r.json()).job.id;
	}
	if (ids.nightly) {
		ok(
			await page.request.put(`/api/v1/jobs/${ids.nightly}/access-rules`, {
				data: { rules: [{ role: 'runner', rule: "'extra-runners' in claims.groups" }] },
				headers: h
			})
		);
	}
});

const treeRoots = (page: Page) => page.locator('[data-testid^="tree-project-tree-"]');

test('an instance admin sees every project and expands one level at a time', async ({
	browser
}) => {
	const page = await as(browser, 'alice-sub');
	await expect(page.getByTestId('tree-toggle-tree-acme')).toBeVisible();
	await expect(page.getByTestId('tree-toggle-tree-ops')).toBeVisible();
	await expect(page.getByTestId('tree-toggle-tree-acme/pay')).toHaveCount(0); // not loaded yet
	await page.getByTestId('tree-toggle-tree-acme').click();
	await expect(page.getByTestId('tree-toggle-tree-acme/pay')).toBeVisible();
	await expect(page.getByTestId('tree-job-tree-acme/deploy')).toBeVisible();
	await expect(page.getByTestId('tree-role-tree-acme')).toHaveText('admin');
	// The open state survives a reload.
	await page.reload();
	await expect(page.getByTestId('tree-job-tree-acme/deploy')).toBeVisible();
	await page.getByTestId('tree-job-tree-acme/deploy').click();
	await expect(page).toHaveURL(/\/jobs\/[^/]+$/);
});

test('a runner sees the subtree they hold a role on, inherited downward', async ({ browser }) => {
	const page = await as(browser, 'bob-sub');
	await expect(page.getByTestId('tree-toggle-tree-acme')).toBeVisible();
	await expect(page.getByTestId('tree-role-tree-acme')).toHaveText('runner');
	await expect(page.getByTestId('tree-toggle-tree-ops')).toHaveCount(0);
	await page.getByTestId('tree-toggle-tree-acme').click();
	await expect(page.getByTestId('tree-role-tree-acme/pay')).toHaveText('runner');
	await page.getByTestId('tree-toggle-tree-acme/pay').click();
	await expect(page.getByTestId('tree-job-tree-acme/pay/charge')).toBeVisible();
});

test('a role on a child alone gives a root labelled with its path, and no parent', async ({
	browser
}) => {
	const page = await as(browser, 'dave-sub');
	await expect(page.getByTestId('tree-link-tree-acme/pay')).toHaveText('tree-acme/pay');
	await expect(page.getByTestId('tree-role-tree-acme/pay')).toHaveText('editor');
	await expect(page.getByTestId('tree-toggle-tree-acme')).toHaveCount(0);
	await expect(treeRoots(page)).toHaveCount(1);
	// The server says the same as the page: the parent is just not found.
	expect((await page.request.get('/api/v1/tree?parent=tree-acme')).status()).toBe(404);
	expect((await page.request.get('/api/v1/tree?parent=tree-nope')).status()).toBe(404);
});

test('a job-rule-only user sees the job under Shared with me and no project node', async ({
	browser
}) => {
	const page = await as(browser, 'frank-sub');
	await expect(page.getByTestId('tree-shared-nightly')).toBeVisible();
	await expect(treeRoots(page)).toHaveCount(0);
	await expect(page.getByTestId('tree-toggle-tree-ops')).toHaveCount(0);
	await expect(page.getByText('tree-ops')).toHaveCount(0); // the project is not named anywhere
	await page.getByTestId('tree-shared-nightly').click();
	await expect(page).toHaveURL(/\/jobs\/[^/]+$/);
});

test('a user with no role sees nothing of the tree', async ({ browser }) => {
	const page = await as(browser, 'erin-sub');
	await expect(page.getByTestId('project-tree')).toBeVisible();
	await expect(treeRoots(page)).toHaveCount(0);
	await expect(page.getByTestId('tree-shared')).toHaveCount(0);
});

const rows = (page: Page) => page.getByTestId('job-row');

test('the jobs page lists every visible job and a project narrows it to its subtree', async ({
	browser
}) => {
	const page = await as(browser, 'alice-sub');
	await page.goto('/jobs');
	for (const name of ['deploy', 'charge', 'nightly'])
		await expect(rows(page).filter({ hasText: name })).toHaveCount(1);
	// Choosing a project in the tree lists its jobs and those below it, nothing else.
	await page.getByTestId('tree-link-tree-acme').click();
	await expect(page).toHaveURL(/\/projects\/tree-acme$/);
	await expect(rows(page).filter({ hasText: 'deploy' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'charge' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'nightly' })).toHaveCount(0);
	await expect(page.getByTestId('scope-project')).toHaveText('tree-acme and below');
	// A breadcrumb shows where the project sits; going to a child narrows further.
	await page.goto('/projects/tree-acme/pay');
	await expect(page.getByTestId('crumb-tree-acme')).toBeVisible();
	await expect(rows(page).filter({ hasText: 'charge' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'deploy' })).toHaveCount(0);
	await page.getByTestId('scope-all').click();
	await expect(page).toHaveURL(/\/jobs$/);
	await expect(rows(page).filter({ hasText: 'nightly' })).toHaveCount(1);
});

test('the jobs page shows only what the caller may see, and a hidden project reads as missing', async ({
	browser
}) => {
	const dave = await as(browser, 'dave-sub');
	await dave.goto('/jobs');
	await expect(rows(dave).filter({ hasText: 'charge' })).toHaveCount(1);
	await expect(rows(dave).filter({ hasText: 'deploy' })).toHaveCount(0);
	await expect(rows(dave).filter({ hasText: 'nightly' })).toHaveCount(0);
	await dave.goto('/projects/tree-acme');
	await expect(dave.getByTestId('jobs-error')).toContainText(
		'Not found, or you may not have access'
	);
	const missing = await as(browser, 'dave-sub');
	await missing.goto('/projects/tree-does-not-exist');
	await expect(missing.getByTestId('jobs-error')).toContainText(
		'Not found, or you may not have access'
	);

	// frank holds a role on the job only: not in the jobs list, only under "Shared with me".
	const frank = await as(browser, 'frank-sub');
	await frank.goto('/jobs');
	await expect(rows(frank).filter({ hasText: 'nightly' })).toHaveCount(0);
	await expect(frank.getByTestId('tree-shared-nightly')).toBeVisible();
});

async function jobId(browser: Browser, name: string): Promise<string> {
	const alice = await as(browser, 'alice-sub');
	const list = await (await alice.request.get('/api/v1/tree/jobs?limit=200')).json();
	return list.items.find((j: { name: string }) => j.name === name).id;
}

test('a viewer sees Run disabled with the missing role and no trigger forms', async ({
	browser
}) => {
	const id = await jobId(browser, 'deploy');
	const carol = await as(browser, 'carol-sub');
	await carol.goto(`/jobs/${id}`);
	await expect(carol.getByTestId('job-title')).toHaveText('deploy');
	await expect(carol.getByTestId('run-job')).toBeDisabled();
	await expect(carol.getByTestId('run-needs')).toHaveText('needs runner');
	await expect(carol.getByTestId('job-triggers')).toHaveCount(0); // admin-only: absent
	await expect(carol.getByTestId('schedule-form')).toHaveCount(0);
	await expect(carol.getByTestId('webhook-form')).toHaveCount(0);

	const bob = await as(browser, 'bob-sub');
	await bob.goto(`/jobs/${id}`);
	await expect(bob.getByTestId('run-job')).toBeEnabled();
	await expect(bob.getByTestId('run-needs')).toHaveCount(0);
	await expect(bob.getByTestId('job-triggers')).toHaveCount(0);

	const alice = await as(browser, 'alice-sub');
	await alice.goto(`/jobs/${id}`);
	await expect(alice.getByTestId('run-job')).toBeEnabled();
	await expect(alice.getByTestId('job-triggers')).toBeVisible();
});

test('a user with no role gets a not-found page with a way back and no hint', async ({
	browser
}) => {
	const id = await jobId(browser, 'deploy');
	const erin = await as(browser, 'erin-sub');
	await erin.goto(`/jobs/${id}`);
	await expect(erin.getByTestId('not-found')).toBeVisible();
	await expect(erin.getByTestId('not-found')).toContainText('may not exist, or you may not have');
	await expect(erin.getByTestId('job-title')).toHaveText('');
	await expect(erin.getByTestId('job-error')).toHaveCount(0);
	const gone = await as(browser, 'erin-sub');
	await gone.goto('/jobs/00000000-0000-7000-8000-00000000dead');
	await expect(gone.getByTestId('not-found')).toBeVisible(); // the same page for a missing job
	await erin.getByRole('link', { name: 'Jobs' }).first().click();
	await expect(erin).toHaveURL(/\/jobs$/);
	await gone.goto('/runs/00000000-0000-7000-8000-00000000dead');
	await expect(gone.getByTestId('not-found')).toBeVisible();
});

test("tree counts cover each caller's visible subtree once and differ between callers", async ({
	browser
}) => {
	const alice = await as(browser, 'alice-sub');
	const level = async (parent: string) =>
		(await (await alice.request.get(`/api/v1/tree?parent=${parent}`)).json()).projects;
	const failedOf = async () =>
		(await level('tree-acme')).find((p: { path: string }) => p.path === 'tree-acme/pay').failed_24h;
	const before = await failedOf();
	const me = await (await alice.request.get('/api/v1/me')).json();
	const jobs = (await (await alice.request.get('/api/v1/tree/jobs?scope=tree-acme/pay')).json())
		.items;
	const run = await alice.request.post(`/api/v1/jobs/${jobs[0].id}/runs`, {
		data: {},
		headers: { 'X-CSRF-Token': me.csrf_token }
	});
	expect(run.status()).toBe(201);
	// The dev agent has no inventory, so the run fails at once.
	await expect.poll(failedOf).toBe(before + 1);

	// bob holds a role on tree-acme: both jobs, and the failure once (no double count).
	const bob = await as(browser, 'bob-sub');
	await expect(bob.getByTestId('tree-jobs-tree-acme')).toHaveText('2');
	await expect(bob.getByTestId('tree-failed-tree-acme')).toHaveText(`${before + 1} failed`);
	// dave holds a role on tree-acme/pay alone: one job, the same failures, a different tree.
	const dave = await as(browser, 'dave-sub');
	await expect(dave.getByTestId('tree-jobs-tree-acme/pay')).toHaveText('1');
	await expect(dave.getByTestId('tree-failed-tree-acme/pay')).toHaveText(`${before + 1} failed`);
	await expect(dave.getByTestId('tree-jobs-tree-acme')).toHaveCount(0);
	// An admin sees the other project's jobs as well.
	await expect(alice.getByTestId('tree-jobs-tree-ops')).toHaveText('1');
});

test("the home page shows recent runs of the caller's projects only, failures first, and entry points", async ({
	browser
}) => {
	// A failed run in each project (the dev agent has no inventory, so runs fail at once).
	const alice = await as(browser, 'alice-sub');
	const me = await (await alice.request.get('/api/v1/me')).json();
	const jobs = (await (await alice.request.get('/api/v1/tree/jobs?limit=200')).json()).items;
	for (const name of ['deploy', 'charge', 'nightly']) {
		const j = jobs.find(
			(x: { name: string; project: string }) => x.name === name && x.project.startsWith('tree-')
		);
		const r = await alice.request.post(`/api/v1/jobs/${j.id}/runs`, {
			data: {},
			headers: { 'X-CSRF-Token': me.csrf_token }
		});
		expect(r.status()).toBe(201);
	}
	// Wait until the three runs have failed, so the first page load already shows them.
	await expect
		.poll(
			async () => {
				const items = (await (await alice.request.get('/api/v1/runs?limit=25')).json()).items;
				return items.filter((r: { status: string }) => r.status === 'failed').length >= 3;
			},
			{ timeout: 30_000 }
		)
		.toBe(true);
	const texts = async (page: Page) =>
		(await page.getByTestId('home-run').allTextContents()).join('\n');

	await alice.goto('/');
	await expect(alice.getByTestId('home-failures')).toBeVisible();
	await expect(alice.getByTestId('home-runs')).toContainText('tree-ops / nightly');
	await expect(alice.getByTestId('home-project-tree-acme')).toBeVisible();

	const bob = await as(browser, 'bob-sub');
	await bob.goto('/');
	await expect(bob.getByTestId('home-runs')).toContainText('tree-acme / deploy');
	await expect(bob.getByTestId('home-runs')).toContainText('tree-acme/pay / charge');
	expect(await texts(bob)).not.toContain('tree-ops');
	await expect(bob.getByTestId('home-project-tree-acme')).toBeVisible();
	await expect(bob.getByTestId('home-project-tree-ops')).toHaveCount(0);

	const dave = await as(browser, 'dave-sub');
	await dave.goto('/');
	await expect(dave.getByTestId('home-runs')).toContainText('tree-acme/pay / charge');
	expect(await texts(dave)).not.toContain('deploy');
	expect(await texts(dave)).not.toContain('nightly');
	await dave.getByTestId('home-project-tree-acme/pay').click();
	await expect(dave).toHaveURL(/\/projects\/tree-acme\/pay$/);

	// A user with no role: an empty state, no runs, no project.
	const erin = await as(browser, 'erin-sub');
	await erin.goto('/');
	await expect(erin.getByTestId('home-empty')).toBeVisible();
	await expect(erin.getByTestId('home-run')).toHaveCount(0);

	// A job-rule-only user has no runs here but a way in to the shared job.
	const frank = await as(browser, 'frank-sub');
	await frank.goto('/');
	await expect(frank.getByTestId('home-empty')).toHaveCount(0);
	await expect(frank.getByTestId('home-shared-nightly')).toBeVisible();
	await expect(frank.getByTestId('home-run')).toHaveCount(0);
});
