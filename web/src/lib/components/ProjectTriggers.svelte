<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { formatWhen } from '$lib/format';
	import { jobRest } from '$lib/jobroute';
	import { type Job } from '$lib/jobs';
	import { poll } from '$lib/polling';
	import { atLeast } from '$lib/projects';
	import { fetchProjectJobs } from '$lib/projectruns';
	import { SESSION_TOKEN } from '$lib/session';
	import {
		deleteSchedule,
		fetchProjectSchedules,
		fetchProjectWebhooks,
		problemMessage,
		revokeWebhook,
		setScheduleEnabled,
		type Schedule,
		type Webhook
	} from '$lib/triggers';

	/**
	 * Every schedule, or every webhook, of the jobs of a project and the projects
	 * below it. Pausing and deleting a schedule needs editor, revoking a webhook
	 * admin (ADR-0076); adding either is done on the job's own tab.
	 */
	let {
		path,
		kind,
		role,
		roleLoaded = true
	}: {
		path: string;
		kind: 'schedules' | 'webhooks';
		role?: string;
		roleLoaded?: boolean;
	} = $props();

	const token = SESSION_TOKEN;
	const canSchedule = $derived(atLeast(role, 'editor'));
	const canHook = $derived(atLeast(role, 'admin'));

	let schedules = $state<Schedule[]>([]);
	let webhooks = $state<Webhook[]>([]);
	let jobs = $state(new Map<string, Job>());
	let loaded = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);

	async function load(signal?: AbortSignal) {
		try {
			const [rows, byId] = await Promise.all([
				kind === 'schedules'
					? fetchProjectSchedules(token, path)
					: fetchProjectWebhooks(token, path),
				fetchProjectJobs(path, signal)
			]);
			if (signal?.aborted) return;
			if (kind === 'schedules') schedules = rows as Schedule[];
			else webhooks = rows as Webhook[];
			jobs = byId;
			error = null;
		} catch (e) {
			if (!signal?.aborted) error = problemMessage(e);
		} finally {
			if (!signal?.aborted) loaded = true;
		}
	}

	async function act(fn: () => Promise<void>) {
		busy = true;
		try {
			await fn();
			await load();
		} catch (e) {
			error = problemMessage(e);
		} finally {
			busy = false;
		}
	}

	onMount(() => poll((signal) => load(signal), 15000));

	const jobName = (id: string) => jobs.get(id)?.name ?? id;
	const jobHref = (id: string, tab: string) =>
		resolve('/projects/[...path]', { path: jobRest(jobs.get(id)?.project ?? path, id, tab) });
</script>

<div class="space-y-6" data-testid="project-triggers">
	<h1 class="text-2xl font-semibold" data-testid="project-title">{path}</h1>
	<p class="text-sm text-muted-foreground">
		{kind === 'schedules'
			? 'Schedules of the jobs of this project and the projects below it. Add one on the job’s Schedule tab.'
			: 'Webhooks of the jobs of this project and the projects below it. Add one on the job’s Webhooks tab.'}
	</p>

	{#if error}
		<Alert.Root variant="destructive" data-testid="triggers-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}

	{#if !loaded}
		<Skeleton class="h-24 w-full" />
	{:else if kind === 'schedules'}
		<Table.Root data-testid="schedules">
			<Table.Header>
				<Table.Row>
					<Table.Head>Job</Table.Head>
					<Table.Head>Name</Table.Head>
					<Table.Head>Cron</Table.Head>
					<Table.Head>State</Table.Head>
					<Table.Head>Next run</Table.Head>
					<Table.Head></Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each schedules as s (s.id)}
					<Table.Row data-testid="schedule-row">
						<Table.Cell>
							<a class="underline-offset-4 hover:underline" href={jobHref(s.job_id, 'schedule')}
								>{jobName(s.job_id)}</a
							>
						</Table.Cell>
						<Table.Cell>{s.name}</Table.Cell>
						<Table.Cell><code>{s.cron}</code> {s.timezone}</Table.Cell>
						<Table.Cell>
							<Badge variant={s.enabled ? 'default' : 'secondary'} data-testid="schedule-state"
								>{s.enabled ? 'enabled' : 'paused'}</Badge
							>
						</Table.Cell>
						<Table.Cell data-testid="schedule-next">{formatWhen(s.next_fire_at)}</Table.Cell>
						<Table.Cell class="space-x-2 text-right">
							{#if canSchedule}
								<Button
									variant="outline"
									size="sm"
									disabled={busy}
									data-testid="schedule-toggle"
									onclick={() => act(() => setScheduleEnabled(token, s.id, !s.enabled))}
									>{s.enabled ? 'Pause' : 'Resume'}</Button
								>
								<Button
									variant="outline"
									size="sm"
									disabled={busy}
									data-testid="schedule-delete"
									onclick={() => act(() => deleteSchedule(token, s.id))}>Delete</Button
								>
							{/if}
						</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row>
						<Table.Cell colspan={6} class="text-muted-foreground">No schedules.</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
		{#if roleLoaded && !canSchedule}
			<p class="text-sm text-muted-foreground" data-testid="schedule-needs">
				Changing schedules needs the editor role.
			</p>
		{/if}
	{:else}
		<Table.Root data-testid="webhooks">
			<Table.Header>
				<Table.Row>
					<Table.Head>Job</Table.Head>
					<Table.Head>Name</Table.Head>
					<Table.Head>State</Table.Head>
					<Table.Head>Last used</Table.Head>
					<Table.Head></Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each webhooks as w (w.id)}
					<Table.Row data-testid="webhook-row">
						<Table.Cell>
							<a class="underline-offset-4 hover:underline" href={jobHref(w.job_id, 'webhooks')}
								>{jobName(w.job_id)}</a
							>
						</Table.Cell>
						<Table.Cell>{w.name}</Table.Cell>
						<Table.Cell>
							<Badge variant={w.revoked ? 'secondary' : 'default'} data-testid="webhook-state"
								>{w.revoked ? 'revoked' : 'active'}</Badge
							>
						</Table.Cell>
						<Table.Cell>{formatWhen(w.last_used_at)}</Table.Cell>
						<Table.Cell class="text-right">
							{#if canHook && !w.revoked}
								<Button
									variant="outline"
									size="sm"
									disabled={busy}
									data-testid="webhook-revoke"
									onclick={() => act(() => revokeWebhook(token, w.id))}>Revoke</Button
								>
							{/if}
						</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row>
						<Table.Cell colspan={5} class="text-muted-foreground">No webhooks.</Table.Cell>
					</Table.Row>
				{/each}
			</Table.Body>
		</Table.Root>
		{#if roleLoaded && !canHook}
			<p class="text-sm text-muted-foreground" data-testid="webhook-needs">
				Revoking webhooks needs the admin role on the project.
			</p>
		{/if}
	{/if}
</div>
