import { expect, test, type Browser, type Page } from '@playwright/test';

// The effective-access view (see playwright.oidc.config.ts). tree.test.ts builds
// the tree-acme projects and jobs these tests look at; files run in order.
const idp = 'http://127.0.0.1:4191';

async function as(browser: Browser, sub: string): Promise<Page> {
	const ctx = await browser.newContext();
	expect((await ctx.request.post(`${idp}/_login-as?sub=${sub}`)).status()).toBe(204);
	const page = await ctx.newPage();
	await page.goto('/access');
	await expect(page.getByTestId('user-name')).toBeVisible();
	return page;
}

const where = (page: Page, w: string) =>
	page.locator(`[data-testid="my-access-row"][data-where="${w}"]`);

test.describe.configure({ mode: 'serial' });

test('my access shows each role and the rule that gave it, to any signed-in user', async ({
	browser
}) => {
	const bob = await as(browser, 'bob-sub');
	await expect(where(bob, 'tree-acme')).toContainText('runner');
	await expect(where(bob, 'tree-acme').getByTestId('my-access-source')).toContainText(
		'a rule of tree-acme (rule 1)'
	);
	await expect(where(bob, 'tree-acme/pay').getByTestId('my-access-source')).toContainText(
		'an ancestor'
	);
	await expect(bob.getByTestId('access-check')).toHaveCount(0); // not for a non-admin
	await expect(where(bob, 'tree-ops')).toHaveCount(0);

	const dave = await as(browser, 'dave-sub');
	await expect(where(dave, 'tree-acme/pay').getByTestId('my-access-source')).toContainText(
		'a rule of tree-acme/pay'
	);
	await expect(where(dave, 'tree-acme')).toHaveCount(0);

	const frank = await as(browser, 'frank-sub');
	await expect(frank.getByTestId('my-access-row')).toHaveCount(1);
	await expect(frank.getByTestId('my-access-row')).toContainText('job nightly');
	await expect(frank.getByTestId('my-access-source')).toContainText('the job itself');

	const erin = await as(browser, 'erin-sub');
	await expect(erin.getByTestId('access-none')).toBeVisible();
});

test('an admin asks why a group holds a role, and the check is audited', async ({ browser }) => {
	const alice = await as(browser, 'alice-sub');
	await expect(where(alice, 'tree-acme').getByTestId('my-access-source')).toContainText(
		'instance admin'
	);
	// The check lives in the Admin area.
	await alice.getByTestId('admin-gear').click();
	await alice.getByTestId('admin-nav-access-check').click();
	await expect(alice).toHaveURL(/\/admin\/access-check$/);
	await expect(alice.getByTestId('access-check')).toBeVisible();

	// bob's group on the child: the rule of the ancestor, named with its text.
	await alice.getByTestId('check-groups').fill('team-ops');
	await alice.getByTestId('check-project').fill('tree-acme/pay');
	await alice.getByTestId('check-run').click();
	await expect(alice.getByTestId('check-role')).toHaveText('runner');
	await expect(alice.getByTestId('check-source')).toContainText('an ancestor');
	await expect(alice.getByTestId('check-source')).toContainText('tree-acme');
	await expect(alice.getByTestId('check-rule')).toContainText("'team-ops' in claims.groups");

	// A group with no rule: no role.
	await alice.getByTestId('check-groups').fill('some-other-group');
	await alice.getByTestId('check-run').click();
	await expect(alice.getByTestId('check-none')).toBeVisible();

	// A project that does not exist: an error, not a result.
	await alice.getByTestId('check-project').fill('tree-nope');
	await alice.getByTestId('check-run').click();
	await expect(alice.getByTestId('check-error')).toBeVisible();

	// The checks are in the audit log, without the groups.
	const audit = await (await alice.request.get('/api/v1/audit?limit=200')).json();
	const checks = audit.items.filter((e: { action: string }) => e.action === 'access.check');
	expect(checks.length).toBeGreaterThanOrEqual(2);
	expect(JSON.stringify(checks)).not.toContain('some-other-group');
	expect(JSON.stringify(checks)).not.toContain('team-ops');
});
