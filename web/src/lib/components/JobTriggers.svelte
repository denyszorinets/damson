<script lang="ts">
	import { SESSION_TOKEN } from '$lib/session';
	// Schedules and webhooks of a job. Both are admin operations until M8, so the
	// section asks for the admin token (kept in this tab only).
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import TokenGate from '$lib/components/TokenGate.svelte';
	import {
		Unauthorized,
		createSchedule,
		createWebhook,
		deleteSchedule,
		fetchSchedules,
		fetchWebhooks,
		loadToken,
		problemMessage,
		revokeWebhook,
		saveToken,
		setScheduleEnabled,
		type Schedule,
		type Webhook
	} from '$lib/triggers';

	let { jobId }: { jobId: string } = $props();

	let token = $state('');
	let schedules = $state<Schedule[]>([]);
	let webhooks = $state<Webhook[]>([]);
	let error = $state<string | null>(null);
	let busy = $state(false);
	// The secret path of a webhook just created: shown once, then dropped.
	let newHookPath = $state<string | null>(null);

	let schedName = $state('');
	let schedCron = $state('0 3 * * *');
	let schedZone = $state('UTC');
	let hookName = $state('');
	let hookMapping = $state('');

	function fail(e: unknown) {
		if (e instanceof Unauthorized) {
			forget();
			error = 'The admin token was rejected. Enter a valid token.';
		} else {
			error = problemMessage(e);
		}
	}

	function forget() {
		token = '';
		saveToken('');
		schedules = [];
		webhooks = [];
		newHookPath = null;
	}

	async function load() {
		if (!token) return;
		try {
			[schedules, webhooks] = await Promise.all([
				fetchSchedules(token, jobId),
				fetchWebhooks(token, jobId)
			]);
			error = null;
		} catch (e) {
			fail(e);
		}
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		try {
			await fn();
			error = null;
			await load();
		} catch (e) {
			fail(e);
		} finally {
			busy = false;
		}
	}

	const addSchedule = (e: Event) => {
		e.preventDefault();
		void act(async () => {
			await createSchedule(token, {
				job_id: jobId,
				name: schedName.trim(),
				cron: schedCron.trim(),
				timezone: schedZone.trim() || 'UTC',
				options: {}
			});
			schedName = '';
		});
	};

	const addWebhook = (e: Event) => {
		e.preventDefault();
		void act(async () => {
			const created = await createWebhook(token, {
				job_id: jobId,
				name: hookName.trim(),
				options_mapping: hookMapping.trim() || undefined
			});
			newHookPath = created.path;
			hookName = hookMapping = '';
		});
	};

	onMount(() => {
		token = loadToken();
		void load();
	});
</script>

<section class="space-y-6" data-testid="job-triggers" aria-label="Triggers">
	<div class="flex items-center justify-between">
		<h2 class="text-lg font-medium">Triggers</h2>
		{#if token}
			{#if token !== SESSION_TOKEN}
				<Button variant="outline" data-testid="forget-token" onclick={forget}>Forget token</Button>
			{/if}
		{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="triggers-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !token}
		<TokenGate
			onsubmit={(t) => {
				token = t;
				saveToken(t);
				void load();
			}}
		/>
	{:else}
		<div class="space-y-3">
			<h3 class="font-medium">Schedules</h3>
			<Table.Root data-testid="schedules">
				<Table.Header>
					<Table.Row>
						<Table.Head>Name</Table.Head>
						<Table.Head>Cron</Table.Head>
						<Table.Head>Next fire</Table.Head>
						<Table.Head>Last result</Table.Head>
						<Table.Head></Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each schedules as s (s.id)}
						<Table.Row data-testid="schedule-row">
							<Table.Cell>{s.name}{s.enabled ? '' : ' (off)'}</Table.Cell>
							<Table.Cell><code>{s.cron}</code> {s.timezone}</Table.Cell>
							<Table.Cell>{s.next_fire_at ?? '-'}</Table.Cell>
							<Table.Cell>{s.last_error ?? s.last_run_id ?? '-'}</Table.Cell>
							<Table.Cell class="space-x-2 text-right">
								<Button
									variant="outline"
									size="sm"
									disabled={busy}
									onclick={() => act(() => setScheduleEnabled(token, s.id, !s.enabled))}
								>
									{s.enabled ? 'Disable' : 'Enable'}
								</Button>
								<Button
									variant="outline"
									size="sm"
									disabled={busy}
									onclick={() => act(() => deleteSchedule(token, s.id))}>Delete</Button
								>
							</Table.Cell>
						</Table.Row>
					{:else}
						<Table.Row
							><Table.Cell colspan={5} class="text-muted-foreground">No schedules.</Table.Cell
							></Table.Row
						>
					{/each}
				</Table.Body>
			</Table.Root>
			<form class="grid max-w-xl gap-2" data-testid="schedule-form" onsubmit={addSchedule}>
				<Label for="sched-name">Name</Label>
				<Input id="sched-name" bind:value={schedName} required />
				<Label for="sched-cron">Cron (5 fields)</Label>
				<Input id="sched-cron" bind:value={schedCron} required />
				<Label for="sched-zone">Time zone</Label>
				<Input id="sched-zone" bind:value={schedZone} />
				<Button type="submit" disabled={busy}>Add schedule</Button>
			</form>
		</div>

		<div class="space-y-3">
			<h3 class="font-medium">Webhooks</h3>
			{#if newHookPath}
				<Alert.Root data-testid="new-webhook">
					<Alert.Description>
						Copy this URL path now; it is not shown again:
						<code class="break-all" data-testid="new-webhook-path">{newHookPath}</code>
						<Button variant="outline" size="sm" onclick={() => (newHookPath = null)}>Done</Button>
					</Alert.Description>
				</Alert.Root>
			{/if}
			<Table.Root data-testid="webhooks">
				<Table.Header>
					<Table.Row>
						<Table.Head>Name</Table.Head>
						<Table.Head>Last used</Table.Head>
						<Table.Head></Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each webhooks as w (w.id)}
						<Table.Row data-testid="webhook-row">
							<Table.Cell>{w.name}{w.revoked ? ' (revoked)' : ''}</Table.Cell>
							<Table.Cell>{w.last_used_at ?? '-'}</Table.Cell>
							<Table.Cell class="text-right">
								{#if !w.revoked}
									<Button
										variant="outline"
										size="sm"
										disabled={busy}
										onclick={() => act(() => revokeWebhook(token, w.id))}>Revoke</Button
									>
								{/if}
							</Table.Cell>
						</Table.Row>
					{:else}
						<Table.Row
							><Table.Cell colspan={3} class="text-muted-foreground">No webhooks.</Table.Cell
							></Table.Row
						>
					{/each}
				</Table.Body>
			</Table.Root>
			<form class="grid max-w-xl gap-2" data-testid="webhook-form" onsubmit={addWebhook}>
				<Label for="hook-name">Name</Label>
				<Input id="hook-name" bind:value={hookName} required />
				<Label for="hook-mapping">Options mapping (CEL, optional)</Label>
				<Input
					id="hook-mapping"
					bind:value={hookMapping}
					placeholder={'{"env": body.environment}'}
				/>
				<Button type="submit" disabled={busy}>Add webhook</Button>
			</form>
		</div>
	{/if}
</section>
