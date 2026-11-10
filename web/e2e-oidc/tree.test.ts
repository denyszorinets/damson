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

const homeRoots = (page: Page) => page.locator('[data-testid^="home-project-tree-"]');
const homeRows = (page: Page) => page.getByTestId('home-project-row');
const rows = (page: Page) => page.getByTestId('job-row');

test('an instance admin sees every project on Home and no job', async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	await expect(page.getByTestId('home-project-tree-acme')).toBeVisible();
	await expect(page.getByTestId('home-project-tree-ops')).toBeVisible();
	await expect(page.getByTestId('home-project-tree-acme/pay')).toHaveCount(0); // a child, not a root
	await expect(page.getByTestId('jobs-table')).toHaveCount(0);
	await expect(page.getByText('charge')).toHaveCount(0); // no job is named on Home
	// The switcher reaches nested projects too.
	await page.getByTestId('project-switcher').click();
	await expect(page.getByTestId('switch-tree-acme/pay')).toBeVisible();
	await page.getByTestId('switch-tree-acme/pay').click();
	await expect(page).toHaveURL(/\/projects\/tree-acme\/pay$/);
	await expect(page.getByTestId('crumb-tree-acme')).toBeVisible();
	await expect(page.getByTestId('crumb-tree-acme/pay')).toBeVisible();
});

test('a runner sees the projects they hold a role on, and the switcher lists the same', async ({
	browser
}) => {
	const page = await as(browser, 'bob-sub');
	await expect(page.getByTestId('home-project-tree-acme')).toBeVisible();
	await expect(homeRows(page).filter({ hasText: 'tree-acme' }).first()).toContainText('runner');
	await expect(page.getByTestId('home-project-tree-ops')).toHaveCount(0);
	await expect(page.getByTestId('jobs-table')).toHaveCount(0);
	await expect(page.getByText('deploy')).toHaveCount(0);
	await page.getByTestId('project-switcher').click();
	await expect(page.getByTestId('switch-tree-acme')).toBeVisible();
	await expect(page.getByTestId('switch-tree-acme/pay')).toBeVisible();
	await expect(page.getByTestId('switch-tree-ops')).toHaveCount(0);
});

test('a role on a child alone gives a root labelled with its path, and no parent', async ({
	browser
}) => {
	const page = await as(browser, 'dave-sub');
	await expect(page.getByTestId('home-project-tree-acme/pay')).toHaveText('tree-acme/pay');
	await expect(homeRoots(page)).toHaveCount(1);
	// The server says the same as the page: the parent is just not found.
	expect((await page.request.get('/api/v1/tree?parent=tree-acme')).status()).toBe(404);
	expect((await page.request.get('/api/v1/tree?parent=tree-nope')).status()).toBe(404);
	await page.getByTestId('project-switcher').click();
	await expect(page.getByTestId('switch-tree-acme/pay')).toBeVisible();
	await expect(page.getByTestId('switch-tree-acme')).toHaveCount(0);
});

test('a job-rule-only user sees the job under Shared with me and no project', async ({
	browser
}) => {
	const page = await as(browser, 'frank-sub');
	await expect(page.getByTestId('home-shared-nightly')).toBeVisible();
	await expect(homeRoots(page)).toHaveCount(0);
	await expect(page.getByText('tree-ops')).toHaveCount(0); // the project is not named anywhere
	await page.getByTestId('home-shared-nightly').click();
	await expect(page).toHaveURL(/\/jobs\/[^/]+$/);
});

test('a user with no role sees nothing', async ({ browser }) => {
	const page = await as(browser, 'erin-sub');
	await expect(page.getByTestId('home-empty')).toBeVisible();
	await expect(homeRoots(page)).toHaveCount(0);
	await expect(page.getByTestId('home-shared')).toHaveCount(0);
});

test('/jobs has no list and lands on Home', async ({ browser }) => {
	const alice = await as(browser, 'alice-sub');
	await alice.goto('/jobs');
	await expect(alice).toHaveURL(/\/$/);
	await expect(alice.getByTestId('home-projects')).toBeVisible();
});

test('a project page lists its jobs and those below it, with a breadcrumb', async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	await page.getByTestId('home-project-tree-acme').click();
	await expect(page).toHaveURL(/\/projects\/tree-acme$/);
	await expect(rows(page).filter({ hasText: 'deploy' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'charge' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'nightly' })).toHaveCount(0);
	await page.goto('/projects/tree-acme/pay');
	await expect(page.getByTestId('crumb-tree-acme')).toBeVisible();
	await expect(rows(page).filter({ hasText: 'charge' })).toHaveCount(1);
	await expect(rows(page).filter({ hasText: 'deploy' })).toHaveCount(0);
	// A tab segment that does not exist is the not-found view.
	await page.goto('/projects/tree-acme/-/nope');
	await expect(page.getByTestId('not-found')).toBeVisible();
});

test('a hidden project reads as missing on its URL', async ({ browser }) => {
	const dave = await as(browser, 'dave-sub');
	await dave.goto('/projects/tree-acme/pay');
	await expect(rows(dave).filter({ hasText: 'charge' })).toHaveCount(1);
	await dave.goto('/projects/tree-acme');
	await expect(dave.getByTestId('jobs-error')).toContainText(
		'Not found, or you may not have access'
	);
	const missing = await as(browser, 'dave-sub');
	await missing.goto('/projects/tree-does-not-exist');
	await expect(missing.getByTestId('jobs-error')).toContainText(
		'Not found, or you may not have access'
	);
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
	await erin.getByRole('link', { name: 'Home' }).first().click();
	await expect(erin).toHaveURL(/\/$/);
	await gone.goto('/runs/00000000-0000-7000-8000-00000000dead');
	await expect(gone.getByTestId('not-found')).toBeVisible();
});

test("home counts cover each caller's visible subtree once and differ between callers", async ({
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
	await expect(bob.getByTestId('home-jobs-tree-acme')).toHaveText('2');
	await expect(bob.getByTestId('home-failed-tree-acme')).toHaveText(`${before + 1}`);
	// dave holds a role on tree-acme/pay alone: one job, the same failures, a different list.
	const dave = await as(browser, 'dave-sub');
	await expect(dave.getByTestId('home-jobs-tree-acme/pay')).toHaveText('1');
	await expect(dave.getByTestId('home-failed-tree-acme/pay')).toHaveText(`${before + 1}`);
	await expect(dave.getByTestId('home-jobs-tree-acme')).toHaveCount(0);
	// An admin sees the other project's jobs as well.
	await expect(alice.getByTestId('home-jobs-tree-ops')).toHaveText('1');
});

test('Home never lists jobs or runs, and each project is an entry point', async ({ browser }) => {
	const dave = await as(browser, 'dave-sub');
	await expect(dave.getByTestId('home-run')).toHaveCount(0);
	await expect(dave.getByTestId('home-runs')).toHaveCount(0);
	await dave.getByTestId('home-project-tree-acme/pay').click();
	await expect(dave).toHaveURL(/\/projects\/tree-acme\/pay$/);
	await expect(dave.getByTestId('project-title')).toHaveText('tree-acme/pay');
});

test("a project's Activity and Overview list only the runs of its jobs", async ({ browser }) => {
	const alice = await as(browser, 'alice-sub');
	const me = await (await alice.request.get('/api/v1/me')).json();
	const jobs = (await (await alice.request.get('/api/v1/tree/jobs?limit=200')).json()).items;
	for (const name of ['charge', 'nightly']) {
		const j = jobs.find(
			(x: { name: string; project: string }) => x.name === name && x.project.startsWith('tree-')
		);
		const r = await alice.request.post(`/api/v1/jobs/${j.id}/runs`, {
			data: {},
			headers: { 'X-CSRF-Token': me.csrf_token }
		});
		expect(r.status()).toBe(201);
	}
	const names = async (page: Page) =>
		(await page.getByTestId('run-row').allTextContents()).join('\n');

	// The dev agent has no inventory, so the runs fail at once.
	await alice.goto('/projects/tree-acme/pay/-/activity');
	await expect(alice.getByTestId('run-row').first()).toBeVisible();
	expect(await names(alice)).toContain('charge');
	expect(await names(alice)).not.toContain('nightly');
	expect(await names(alice)).not.toContain('deploy');
	// The status filter narrows the list; the project filter stays.
	await alice.getByTestId('filter-status').click();
	await alice.getByRole('option', { name: 'succeeded' }).click();
	await expect(alice.getByTestId('run-row')).toHaveCount(0);
	await alice.getByTestId('filter-status').click();
	await alice.getByRole('option', { name: 'failed' }).click();
	await expect(alice.getByTestId('run-row').first()).toBeVisible();

	await alice.goto('/projects/tree-acme/pay/-/overview');
	await expect(alice.getByTestId('success-rate')).toContainText('0%');
	await expect(alice.getByTestId('run-row').first()).toContainText('charge');

	// dave holds a role on tree-acme/pay alone and sees its runs there.
	const dave = await as(browser, 'dave-sub');
	await dave.goto('/projects/tree-acme/pay/-/activity');
	await expect(dave.getByTestId('run-row').first()).toBeVisible();
	expect(await names(dave)).not.toContain('nightly');

	// bob is a runner on tree-acme: another project's runs read as missing, in the page and the API.
	const bob = await as(browser, 'bob-sub');
	await bob.goto('/projects/tree-ops/-/activity');
	await expect(bob.getByTestId('activity-error')).toBeVisible();
	await expect(bob.getByTestId('run-row')).toHaveCount(0);
	expect((await bob.request.get('/api/v1/runs?project=tree-ops')).status()).toBe(404);
	expect((await bob.request.get('/api/v1/runs?project=tree-acme')).status()).toBe(200);
});

const spec = (project: string, name: string, command: string, steps?: unknown[]) =>
	JSON.stringify(
		{
			apiVersion: 'damson.dev/v1',
			kind: 'Job',
			metadata: { project, name },
			spec: { steps: steps ?? [{ id: 's', plugin: 'ssh', with: { node: 'n', command } }] }
		},
		null,
		2
	);

test('an editor creates a job and a second version from the UI, with server validation', async ({
	browser
}) => {
	const name = `made-in-ui-${Date.now()}`;
	const dave = await as(browser, 'dave-sub'); // editor on tree-acme/pay
	await dave.goto('/projects/tree-acme/pay');
	await dave.getByTestId('new-job').click();
	await expect(dave.getByTestId('editor-title')).toHaveText('New job');
	await expect(dave.getByTestId('spec-text')).toHaveValue(/"project": "tree-acme\/pay"/);

	// A spec error points at its path; nothing is created.
	await dave.getByTestId('spec-text').fill(spec('tree-acme/pay', name, 'echo one', []));
	await dave.getByTestId('spec-validate').click();
	await expect(dave.getByTestId('spec-issue').first()).toContainText('steps');
	await expect(dave.getByTestId('spec-no-graph')).toBeVisible();
	await dave.getByTestId('spec-save').click();
	await expect(dave.getByTestId('spec-issue').first()).toContainText('steps');
	await expect(dave).toHaveURL(/\/jobs\/new/);

	// Text that is not JSON is reported without a request.
	await dave.getByTestId('spec-text').fill('{ not json');
	await dave.getByTestId('spec-validate').click();
	await expect(dave.getByTestId('spec-issue').first()).toContainText('(document)');

	// A valid spec: the server's graph appears beside the editor; saving creates the job.
	await dave.getByTestId('spec-text').fill(spec('tree-acme/pay', name, 'echo one'));
	await dave.getByTestId('spec-validate').click();
	await expect(dave.getByTestId('spec-valid')).toBeVisible();
	await expect(dave.getByTestId('graph-node-s')).toBeVisible();
	await dave.getByTestId('spec-save').click();
	await expect(dave).toHaveURL(/\/jobs\/[^/]+$/);
	await expect(dave.getByTestId('job-title')).toHaveText(name);

	// A second version, based on the first.
	await dave.getByTestId('edit-job').click();
	await expect(dave.getByTestId('editor-title')).toContainText(`New version of ${name}`);
	await dave.getByTestId('spec-text').fill(spec('tree-acme/pay', name, 'echo two'));
	await dave.getByTestId('spec-save').click();
	await expect(dave).toHaveURL(/\/jobs\/[^/]+$/);
	await expect(dave.getByText('version 2')).toBeVisible();

	// A listing under the project shows it.
	await dave.goto('/projects/tree-acme/pay');
	await expect(rows(dave).filter({ hasText: name })).toHaveCount(1);
});

test('only an editor sees New job and Edit; a managed job is read-only with the reason', async ({
	browser
}) => {
	const id = await jobId(browser, 'deploy');
	for (const sub of ['carol-sub', 'bob-sub']) {
		const p = await as(browser, sub); // viewer and runner on tree-acme
		await p.goto('/projects/tree-acme');
		await expect(p.getByTestId('jobs-table')).toBeVisible();
		await expect(p.getByTestId('new-job')).toHaveCount(0);
		await p.goto(`/jobs/${id}`);
		await expect(p.getByTestId('job-title')).toHaveText('deploy');
		await expect(p.getByTestId('edit-job')).toHaveCount(0);
		await p.goto(`/jobs/${id}/edit`);
		await expect(p.getByTestId('editor-readonly')).toContainText('needs the editor role');
		await expect(p.getByTestId('spec-save')).toHaveCount(0);
		await expect(p.getByTestId('spec-text')).toHaveAttribute('readonly', '');
	}

	// A job a file source manages cannot be changed here, even by an admin.
	const alice = await as(browser, 'alice-sub');
	await alice.route(`**/api/v1/jobs/${id}`, async (route) => {
		const res = await route.fetch();
		const body = await res.json();
		body.job.managed_by = 'files:infra-repo';
		await route.fulfill({ response: res, json: body });
	});
	await alice.goto(`/jobs/${id}/edit`);
	await expect(alice.getByTestId('editor-readonly')).toContainText('managed by files:infra-repo');
	await expect(alice.getByTestId('spec-save')).toHaveCount(0);
});
