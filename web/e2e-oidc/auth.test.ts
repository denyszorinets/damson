import { expect, test, type Browser, type Page } from '@playwright/test';

// Login against the fake provider (see playwright.oidc.config.ts). The provider
// approves at once as the user last chosen with /_login-as.
const idp = 'http://127.0.0.1:4191';

async function as(browser: Browser, sub: string): Promise<Page> {
	const ctx = await browser.newContext();
	const res = await ctx.request.post(`${idp}/_login-as?sub=${sub}`);
	expect(res.status()).toBe(204);
	return ctx.newPage();
}

test.describe.configure({ mode: 'serial' });

test('anonymous visitors are sent to log in and come back', async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	await page.goto('/projects');
	await expect(page.getByTestId('user-name')).toHaveText('alice@example.org');
	await expect(page).toHaveURL(/\/projects$/); // returned to where they were headed
	await expect(page.getByRole('link', { name: 'Secrets' })).toBeVisible(); // alice is an instance admin
});

test('an instance admin builds the tree and writes access rules', async ({ browser }) => {
	const page = await as(browser, 'alice-sub');
	await page.goto('/projects');

	async function create(name: string, parent?: string) {
		// The parent choice stays after a create, so always choose it.
		await page.getByTestId('create-parent').click();
		await page.getByRole('option', { name: parent ?? '(top level)', exact: true }).click();
		await page.getByTestId('create-name').fill(name);
		await page.getByTestId('create-submit').click();
		await expect(
			page.getByTestId(`project-row-${parent ? parent + '/' : ''}${name}`)
		).toBeVisible();
	}
	await create('team');
	await create('web', 'team');
	await create('ops');

	await page.getByTestId('edit-team').click();
	await expect(page.getByTestId('editing-path')).toHaveText('team');

	// A rule that does not compile is refused with its position.
	await page.getByTestId('add-rule').click();
	await page.getByTestId('rule-text-0').fill('claims.groups.(');
	await page.getByTestId('save-rules').click();
	await expect(page.getByTestId('rules-error')).toContainText('rules[0].rule');

	await page.getByTestId('rule-role-0').click();
	await page.getByRole('option', { name: 'runner', exact: true }).click();
	await page.getByTestId('rule-text-0').fill("'team-ops' in claims.groups");
	await page.getByTestId('add-rule').click();
	await page.getByTestId('rule-text-1').fill("'team-viewers' in claims.groups");
	await page.getByTestId('save-rules').click();
	await expect(page.getByTestId('saved')).toHaveText('Access rules saved.');

	// The node scope is checked too, and then saved.
	await page.getByTestId('node-scope').fill('claims.groups');
	await page.getByTestId('save-node-scope').click();
	await expect(page.getByTestId('node-scope-error')).toBeVisible();
	await page.getByTestId('node-scope').fill("'prod' in node.tags");
	await page.getByTestId('save-node-scope').click();
	await expect(page.getByTestId('saved')).toHaveText('Node scope saved.');

	// The rules survive a reload.
	await page.reload();
	await page.getByTestId('edit-team').click();
	await expect(page.getByTestId('rule-text-0')).toHaveValue("'team-ops' in claims.groups");
	await expect(page.getByTestId('node-scope')).toHaveValue("'prod' in node.tags");
});

test('a runner sees only their projects and cannot manage them', async ({ browser }) => {
	const page = await as(browser, 'bob-sub');
	await page.goto('/projects');
	await expect(page.getByTestId('user-name')).toHaveText('bob@example.org');
	await expect(page.getByTestId('role-team')).toHaveText('runner');
	await expect(page.getByTestId('role-team/web')).toHaveText('runner'); // inherited
	await expect(page.getByTestId('project-row-ops')).toHaveCount(0); // out of the tree
	// Nothing to administer: no create form, no delete, no rule editor, no admin pages.
	await expect(page.getByTestId('create-project')).toHaveCount(0);
	await expect(page.getByTestId('delete-team')).toHaveCount(0);
	await expect(page.getByTestId('edit-team')).toHaveCount(0);
	await expect(page.getByRole('link', { name: 'Secrets' })).toHaveCount(0);

	// The server agrees: out-of-tree is not found, writes are refused.
	const me = await (await page.request.get('/api/v1/me')).json();
	expect((await page.request.get('/api/v1/projects/ops')).status()).toBe(404);
	const forbidden = await page.request.put('/api/v1/projects/team/access-rules', {
		data: { rules: [] },
		headers: { 'X-CSRF-Token': me.csrf_token }
	});
	expect(forbidden.status()).toBe(403);
	// And a write without the CSRF token is refused whatever the role.
	const noCsrf = await page.request.post('/api/v1/projects', { data: { path: 'x' } });
	expect(noCsrf.status()).toBe(403);
});

test('a viewer is read-only; a user with no rule sees nothing', async ({ browser }) => {
	const carol = await as(browser, 'carol-sub');
	await carol.goto('/projects');
	await expect(carol.getByTestId('role-team')).toHaveText('viewer');
	await expect(carol.getByTestId('edit-team')).toHaveCount(0);

	const erin = await as(browser, 'erin-sub');
	await erin.goto('/projects');
	await expect(erin.getByTestId('no-projects')).toBeVisible();
});

test('logging out ends the session', async ({ browser }) => {
	const page = await as(browser, 'carol-sub');
	await page.goto('/projects');
	await expect(page.getByTestId('user-name')).toHaveText('carol@example.org');
	// Whoever logs in next is bob: seeing him proves carol's session is gone.
	await page.request.post(`${idp}/_login-as?sub=bob-sub`);
	await page.getByTestId('logout').click();
	await expect(page.getByTestId('user-name')).toHaveText('bob@example.org');
	const me = await (await page.request.get('/api/v1/me')).json();
	expect(me.sub).toBe('bob-sub');
});
