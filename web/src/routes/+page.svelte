<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { poll } from '$lib/polling';
	import { problemMessage } from '$lib/jobs';
	import { fetchRecentRuns, jobsById, type RunSummary } from '$lib/runs';
	import { isUser } from '$lib/session';
	import { fetchLevel, fetchShared, type SharedJob, type TreeProject } from '$lib/tree';

	const REFRESH_MS = 10000;

	let runs = $state<RunSummary[]>([]);
	let names = $state(new Map<string, { name: string; project: string }>());
	let roots = $state<TreeProject[]>([]);
	let shared = $state<SharedJob[]>([]);
	let more = $state(false); // the server stopped scanning before it found 25 runs
	let loaded = $state(false);
	let error = $state<string | null>(null);

	const failed = $derived(runs.filter((r) => r.status === 'failed'));
	// Only a signed-in user can have "no role"; a token operator sees what the token allows.
	const empty = $derived(
		isUser() && loaded && !error && !runs.length && !more && !roots.length && !shared.length
	);

	async function refresh(signal: AbortSignal) {
		try {
			// Entry points come from the caller's tree; without a login there is none to ask.
			const [list, level, sh] = await Promise.all([
				fetchRecentRuns(25, signal),
				isUser() ? fetchLevel('') : Promise.resolve(null),
				isUser() ? fetchShared() : Promise.resolve(null)
			]);
			if (signal.aborted) return;
			names = await jobsById(
				list.items.map((r) => r.job_id),
				signal
			);
			if (signal.aborted) return;
			runs = list.items;
			more = list.more;
			roots = level?.projects ?? [];
			shared = sh?.items ?? [];
			error = null;
		} catch (e) {
			if (!signal.aborted) error = problemMessage(e);
		} finally {
			if (!signal.aborted) loaded = true;
		}
	}

	onMount(() => poll(refresh, REFRESH_MS));

	const label = (r: RunSummary) => {
		const j = names.get(r.job_id);
		return j ? `${j.project} / ${j.name}` : r.job_id;
	};
	const when = (r: RunSummary) => new Date(r.created_at).toLocaleString();
</script>

<svelte:head>
	<title>Damson</title>
</svelte:head>

<main class="mx-auto max-w-4xl space-y-8 px-4 py-8">
	<h1 class="text-2xl font-semibold">Damson</h1>

	{#if error}
		<Alert.Root variant="destructive" data-testid="home-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if !loaded}
		<Skeleton class="h-32 w-full" />
	{:else if empty}
		<section data-testid="home-empty" class="space-y-2">
			<h2 class="text-lg font-medium">Nothing here yet</h2>
			<p class="text-muted-foreground">
				You do not have a role on any project. Ask an administrator for access.
			</p>
		</section>
	{:else}
		{#if failed.length}
			<section class="space-y-2" aria-label="Failures" data-testid="home-failures">
				<h2 class="text-lg font-medium text-destructive">{failed.length} recent failed</h2>
				<ul class="space-y-1 text-sm">
					{#each failed as r (r.id)}
						<li>
							<a
								class="underline underline-offset-4"
								href={resolve('/runs/[id]', { id: r.id })}
								data-testid="home-failure">{label(r)}</a
							>
							<span class="text-muted-foreground">{when(r)}</span>
						</li>
					{/each}
				</ul>
			</section>
		{/if}

		<section class="space-y-2" aria-label="Recent runs">
			<h2 class="text-lg font-medium">Recent runs</h2>
			{#if runs.length}
				<ul class="divide-y rounded-md border text-sm" data-testid="home-runs">
					{#each runs as r (r.id)}
						<li class="flex items-center gap-3 px-3 py-2" data-testid="home-run">
							<Badge
								variant={r.status === 'failed' ? 'destructive' : 'secondary'}
								data-testid="home-run-status">{r.status}</Badge
							>
							<a class="flex-1 truncate hover:underline" href={resolve('/runs/[id]', { id: r.id })}
								>{label(r)}</a
							>
							<span class="text-xs text-muted-foreground">{when(r)}</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="text-sm text-muted-foreground" data-testid="home-no-runs">
					{more
						? 'No recent runs found among the latest ones; open Jobs to look further.'
						: 'No runs yet.'}
				</p>
			{/if}
		</section>

		<section class="space-y-2" aria-label="Your projects">
			<h2 class="text-lg font-medium">Your projects</h2>
			<ul class="flex flex-wrap gap-2 text-sm" data-testid="home-projects">
				<li>
					<a class="rounded-md border px-3 py-1 hover:bg-muted" href={resolve('/jobs')}>All jobs</a>
				</li>
				{#each roots as p (p.path)}
					<li>
						<a
							class="rounded-md border px-3 py-1 hover:bg-muted"
							href={resolve('/projects/[...path]', { path: p.path })}
							data-testid={`home-project-${p.path}`}>{p.label}</a
						>
					</li>
				{/each}
				{#each shared as j (j.id)}
					<li>
						<a
							class="rounded-md border px-3 py-1 hover:bg-muted"
							href={resolve('/jobs/[id]', { id: j.id })}
							data-testid={`home-shared-${j.name}`}>{j.name} (shared)</a
						>
					</li>
				{/each}
			</ul>
		</section>
	{/if}
</main>
