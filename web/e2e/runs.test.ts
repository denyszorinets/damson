import { expect, test, type Page } from '@playwright/test';

// Runs against damson dev (controller + one agent, SQLite); see playwright.config.ts.
// The dev server has no inventory, so every ssh step fails with a validation
// error: enough to see real statuses, retries and a handled group failure.

const step = (id: string, extra: Record<string, unknown> = {}) => ({
	id,
	plugin: 'ssh',
	with: { node: 'n', command: `echo ${id}` },
	...extra
});

async function trigger(page: Page, name: string, steps: unknown[]) {
	const spec = {
		apiVersion: 'damson.dev/v1',
		kind: 'Job',
		metadata: { project: 'default', name },
		spec: { steps }
	};
	const created = await page.request.post('/api/v1/jobs', { data: { spec } });
	expect(created.status()).toBe(201);
	const { job } = await created.json();
	const run = await page.request.post(`/api/v1/jobs/${job.id}/runs`, { data: {} });
	expect(run.status()).toBe(201);
	return (await run.json()).id as string;
}

const node = (page: Page, id: string) => page.getByTestId(`graph-node-${id}`);

test('a real run shows statuses on the graph, survives a reload and ends', async ({ page }) => {
	const errors: string[] = [];
	page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
	const id = await trigger(page, 'run-e2e', [
		{ id: 'g', type: 'group', steps: [step('build')], catch: [step('fix', { when: 'false' })] }
	]);

	await page.goto(`/runs/${id}`);
	// The group's failure is handled by its catch list: the step failed, the
	// catch step was skipped by its condition, the group and the run succeeded.
	await expect(page.getByTestId('run-status')).toHaveText('succeeded');
	await expect(page.getByTestId('run-connection')).toHaveText('closed');
	const expected = { g: 'succeeded', 'g/build': 'failed', 'g/catch/fix': 'skipped' };
	for (const [n, status] of Object.entries(expected)) {
		await expect(node(page, n)).toHaveAttribute('data-status', status);
	}
	await expect(page.getByTestId('instance-row')).toHaveCount(3);

	await page.reload();
	await expect(page.getByTestId('run-status')).toHaveText('succeeded');
	for (const [n, status] of Object.entries(expected)) {
		await expect(node(page, n)).toHaveAttribute('data-status', status);
	}
	expect(errors).toEqual([]);
});

test('a retried step shows its attempt', async ({ page }) => {
	const id = await trigger(page, 'retry-e2e', [
		step('flaky', {
			retry: { max_attempts: 2, retry_on: 'true', backoff: { initial: '100ms', jitter: 0 } }
		})
	]);
	await page.goto(`/runs/${id}`);
	await expect(page.getByTestId('run-status')).toHaveText('failed');
	await expect(node(page, 'flaky')).toHaveAttribute('data-status', 'failed');
	await expect(node(page, 'flaky')).toHaveAttribute('data-attempt', '2');
	await expect(node(page, 'flaky')).toContainText('attempt 2');
});

test('a run that does not exist shows the not-found page', async ({ page }) => {
	await page.goto('/runs/does-not-exist');
	await expect(page.getByTestId('not-found')).toContainText('This run was not found');
});

// A mocked server: a for_each with many lanes is summarized on its nodes and
// the table is bounded.
const lanesGraph = {
	nodes: [
		{ id: '$start', kind: 'start', label: 'start' },
		{
			id: 'g',
			kind: 'group',
			label: 'g',
			for_each: { expr: 'targets' }
		},
		{ id: 'g/for_each[*]/x', kind: 'step', parent: 'g', label: 'x', plugin: 'ssh' },
		{ id: '$end', kind: 'end', label: 'end' }
	],
	edges: [
		{ from: '$start', to: 'g', kind: 'sequence' },
		{ from: 'g', to: '$end', kind: 'sequence' }
	]
};

function lanesSteps(n: number) {
	const steps = [{ path: 'g', node: 'g', lane: '', status: 'succeeded', attempt: 0 }];
	for (let i = 0; i < n; i++) {
		const status = i % 5 === 0 ? 'failed' : 'succeeded';
		steps.push({ path: `g/for_each[${i}]`, node: 'g', lane: String(i), status, attempt: 0 });
		steps.push({
			path: `g/for_each[${i}]/x`,
			node: 'g/for_each[*]/x',
			lane: String(i),
			status,
			attempt: 1
		});
	}
	return steps;
}

const sse = (events: [string, unknown][]) =>
	events
		.map(([name, data], i) => `id: ${i + 1}\nevent: ${name}\ndata: ${JSON.stringify(data)}\n\n`)
		.join('');

test('many lanes are summarized and the instance list is bounded', async ({ page }) => {
	const steps = lanesSteps(250);
	await page.route('**/api/v1/runs/mock/graph', (route) =>
		route.fulfill({ json: { graph: lanesGraph, steps } })
	);
	await page.route('**/api/v1/runs/mock/events', (route) =>
		route.fulfill({
			contentType: 'text/event-stream',
			body: sse([
				[
					'snapshot',
					{
						id: 'mock',
						status: 'failed',
						steps: steps.map((s) => ({
							...s,
							executions: [{ attempt: s.attempt, state: 'failed' }]
						}))
					}
				],
				['end', { status: 'failed', cancel_requested: false }]
			])
		})
	);
	await page.goto('/runs/mock');
	await expect(page.getByTestId('run-status')).toHaveText('failed');
	await expect(page.getByTestId('graph-lanes-g')).toHaveText(/250 lanes: 50 failed, 200 succeeded/);
	await expect(page.getByTestId('graph-lanes-g/for_each[*]/x')).toHaveText(/250 lanes/);
	// 501 instances, 200 rows rendered, the rest counted.
	await expect(page.getByTestId('instance-row')).toHaveCount(200);
	await expect(page.getByTestId('instances-more')).toContainText('301 more');
});

test('a refused stream is reopened', async ({ page }) => {
	const steps = lanesSteps(1);
	await page.route('**/api/v1/runs/flaky/graph', (route) =>
		route.fulfill({ json: { graph: lanesGraph, steps: [] } })
	);
	let calls = 0;
	await page.route('**/api/v1/runs/flaky/events', (route) => {
		calls++;
		if (calls === 1) {
			return route.fulfill({
				status: 503,
				contentType: 'application/problem+json',
				body: JSON.stringify({ title: 'Service Unavailable', status: 503 })
			});
		}
		return route.fulfill({
			contentType: 'text/event-stream',
			body: sse([
				[
					'snapshot',
					{ id: 'flaky', status: 'running', steps: steps.map((s) => ({ ...s, executions: [] })) }
				],
				['end', { status: 'succeeded', cancel_requested: false }]
			])
		});
	});
	await page.goto('/runs/flaky');
	await expect(page.getByTestId('run-connection')).toHaveText('reconnecting');
	await expect(page.getByTestId('run-status')).toHaveText('succeeded');
	expect(calls).toBe(2);
});

test('a run with when, retry and status badges lays out without overlapping boxes', async ({
	page
}) => {
	const steps = Array.from({ length: 6 }, (_, i) =>
		step(`s${i}`, {
			when: 'always()',
			retry: { max_attempts: 2, retry_on: 'true', backoff: { initial: '50ms', jitter: 0 } },
			continue_on_error: i % 2 === 0
		})
	);
	const id = await trigger(page, 'layout-e2e', steps);
	await page.goto(`/runs/${id}`);
	await expect(page.getByTestId('run-status')).toHaveText('failed');
	await expect(node(page, 's5')).toHaveAttribute('data-status', 'failed');
	const boxes = await page.evaluate(() =>
		[...document.querySelectorAll('[data-testid^="graph-node-"]')]
			.filter((e) => ['step', 'start', 'end'].includes(e.getAttribute('data-kind') ?? ''))
			.map((e) => {
				const r = e.getBoundingClientRect();
				return { id: e.getAttribute('data-testid'), x: r.x, y: r.y, w: r.width, h: r.height };
			})
	);
	expect(boxes.length).toBe(8); // start, six steps, end
	for (let i = 0; i < boxes.length; i++)
		for (let j = i + 1; j < boxes.length; j++) {
			const a = boxes[i];
			const b = boxes[j];
			const overlaps = a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
			expect(overlaps, `${a.id} overlaps ${b.id}`).toBe(false);
		}
	// A box also shows all of its content: nothing is clipped.
	const clipped = await page.evaluate(
		() =>
			[...document.querySelectorAll('[data-testid^="graph-node-s"]')].filter(
				(e) => e.scrollHeight > e.clientHeight + 1
			).length
	);
	expect(clipped).toBe(0);
});

// The log viewer is driven through a stand-in for EventSource on the logs URL, so
// a test decides what arrives and when; every other stream stays real.
async function fakeLogStream(page: Page) {
	await page.addInitScript(() => {
		const w = window as unknown as Record<string, unknown>;
		const Real = window.EventSource;
		const urls: string[] = [];
		let current: (EventTarget & { readyState: number; onerror?: () => void }) | undefined;
		function Patched(this: unknown, url: string) {
			if (!/\/executions\/[^/]+\/logs/.test(url)) return new Real(url);
			urls.push(url);
			const es = new EventTarget() as EventTarget & {
				readyState: number;
				onopen?: () => void;
				onerror?: () => void;
				close(): void;
			};
			es.readyState = 1;
			es.close = () => (es.readyState = 2);
			current = es;
			setTimeout(() => es.onopen?.(), 0);
			return es;
		}
		Object.assign(Patched, { CLOSED: 2, OPEN: 1, CONNECTING: 0 });
		(window as unknown as { EventSource: unknown }).EventSource = Patched;
		w.__logUrls = urls;
		w.__emit = (name: string, data: unknown) =>
			current?.dispatchEvent(new MessageEvent(name, { data: JSON.stringify(data) }));
		w.__emitLines = (from: number, to: number) => {
			for (let seq = from; seq <= to; seq++)
				current?.dispatchEvent(
					new MessageEvent('log', {
						data: JSON.stringify({ seq, ts: '', stream: 'stdout', line: `line ${seq}` })
					})
				);
		};
		w.__fail = () => {
			if (current) {
				current.readyState = 2;
				current.onerror?.();
			}
		};
	});
}

test('the log viewer shows live lines, colours, stays bounded, de-duplicates and resumes', async ({
	page
}) => {
	await fakeLogStream(page);
	const id = await trigger(page, 'log-e2e', [step('only')]);
	await page.goto(`/runs/${id}`);
	await expect(page.getByTestId('run-status')).toHaveText('failed');
	await page.getByTestId('open-log').first().click();
	await expect(page.getByTestId('log-viewer')).toBeVisible();

	// Live: lines appear as they arrive, with ANSI colours.
	await page.evaluate(() =>
		(window as unknown as { __emit: (n: string, d: unknown) => void }).__emit('log', {
			seq: 1,
			ts: '',
			stream: 'stdout',
			line: 'start \u001b[31mred part\u001b[0m done'
		})
	);
	await expect(page.getByTestId('log-row')).toHaveCount(1);
	await expect(page.getByTestId('log-row').locator('span.text-red-600')).toHaveText('red part');
	await page.evaluate(() =>
		(window as unknown as { __emitLines: (a: number, b: number) => void }).__emitLines(2, 3)
	);
	await expect(page.getByTestId('log-count')).toHaveText('3 lines');

	// A 100 000 line log: the DOM holds only the rows in view, memory only the cap.
	await page.evaluate(() =>
		(window as unknown as { __emitLines: (a: number, b: number) => void }).__emitLines(4, 100000)
	);
	await expect(page.getByTestId('log-count')).toHaveText('100000 lines');
	await expect(page.getByTestId('log-dropped')).toContainText('earlier not kept');
	const rendered = await page.getByTestId('log-row').count();
	expect(rendered).toBeGreaterThan(0);
	expect(rendered).toBeLessThan(60);
	await expect(page.getByTestId('log-row').last()).toHaveAttribute('data-seq', '100000'); // following

	// A replayed range is applied once.
	await page.evaluate(() =>
		(window as unknown as { __emitLines: (a: number, b: number) => void }).__emitLines(
			99990,
			100000
		)
	);
	await expect(page.getByTestId('log-count')).toHaveText('100000 lines');

	// A refused connection reopens from the last sequence seen.
	await page.evaluate(() => (window as unknown as { __fail: () => void }).__fail());
	await expect
		.poll(() =>
			page.evaluate(() => (window as unknown as { __logUrls: string[] }).__logUrls.length)
		)
		.toBe(2);
	const urls = await page.evaluate(() => (window as unknown as { __logUrls: string[] }).__logUrls);
	expect(urls[0]).toContain('after_seq=0');
	expect(urls[1]).toContain('after_seq=100000');

	await page.evaluate(() =>
		(window as unknown as { __emit: (n: string, d: unknown) => void }).__emit('end', {
			state: 'failed',
			last_seq: 100000
		})
	);
	await expect(page.getByTestId('log-connection')).toHaveText('ended: failed');
});
