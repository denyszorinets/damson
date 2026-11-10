<script lang="ts">
	import { onMount } from 'svelte';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Button } from '$lib/components/ui/button';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { poll } from '$lib/polling';
	import { problemMessage } from '$lib/jobs';
	import { projectRest } from '$lib/projectroute';
	import { isUser } from '$lib/session';
	import { fetchLevel, fetchShared, type SharedJob, type TreeProject } from '$lib/tree';

	const REFRESH_MS = 10000;

	let roots = $state<TreeProject[]>([]);
	let next = $state('');
	let loadingMore = $state(false);
	let shared = $state<SharedJob[]>([]);
	let loaded = $state(false);
	let error = $state<string | null>(null);

	// Only a signed-in user can have "no role"; a token operator sees what the token allows.
	const empty = $derived(isUser() && loaded && !error && !roots.length && !shared.length);

	async function refresh(signal: AbortSignal) {
		try {
			if (!isUser()) {
				// A bearer token has no session for the tree; the Projects page takes the token.
				loaded = true;
				return;
			}
			const [level, sh] = await Promise.all([fetchLevel(''), fetchShared()]);
			if (signal.aborted) return;
			roots = level.projects;
			next = level.next;
			shared = sh?.items ?? [];
			error = null;
		} catch (e) {
			if (!signal.aborted) error = problemMessage(e);
		} finally {
			if (!signal.aborted) loaded = true;
		}
	}

	async function more() {
		loadingMore = true;
		try {
			const level = await fetchLevel('', next);
			roots = [...roots, ...level.projects];
			next = level.next;
		} catch (e) {
			error = problemMessage(e);
		} finally {
			loadingMore = false;
		}
	}

	onMount(() => poll(refresh, REFRESH_MS));
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
	{:else if !isUser()}
		<p class="text-muted-foreground" data-testid="home-token">
			Not signed in. <a class="underline underline-offset-4" href={resolve('/projects')}>Projects</a
			>
			takes an admin token.
		</p>
	{:else if empty}
		<section data-testid="home-empty" class="space-y-2">
			<h2 class="text-lg font-medium">Nothing here yet</h2>
			<p class="text-muted-foreground">
				You do not have a role on any project. Ask an administrator for access.
			</p>
		</section>
	{:else}
		{#if roots.length}
			<section class="space-y-2" aria-label="Projects">
				<h2 class="text-lg font-medium">Projects</h2>
				<Table.Root data-testid="home-projects">
					<Table.Header>
						<Table.Row>
							<Table.Head>Name</Table.Head>
							<Table.Head>Role</Table.Head>
							<Table.Head>Child projects</Table.Head>
							<Table.Head>Jobs</Table.Head>
							<Table.Head>Failed in 24 h</Table.Head>
						</Table.Row>
					</Table.Header>
					<Table.Body>
						{#each roots as p (p.path)}
							<Table.Row data-testid="home-project-row">
								<Table.Cell>
									<a
										class="font-medium underline-offset-4 hover:underline"
										href={resolve('/projects/[...path]', { path: projectRest(p.path) })}
										data-testid={`home-project-${p.path}`}>{p.label}</a
									>
								</Table.Cell>
								<Table.Cell><Badge variant="outline">{p.my_role}</Badge></Table.Cell>
								<Table.Cell>{p.has_children ? 'yes' : 'none'}</Table.Cell>
								<Table.Cell data-testid={`home-jobs-${p.path}`}>{p.jobs ?? '-'}</Table.Cell>
								<Table.Cell data-testid={`home-failed-${p.path}`}>{p.failed_24h ?? '-'}</Table.Cell>
							</Table.Row>
						{/each}
					</Table.Body>
				</Table.Root>
				{#if next}
					<Button variant="outline" onclick={more} disabled={loadingMore} data-testid="home-more"
						>More…</Button
					>
				{/if}
			</section>
		{/if}

		{#if shared.length}
			<section class="space-y-2" aria-label="Shared with me">
				<h2 class="text-lg font-medium">Shared with me</h2>
				<ul class="flex flex-wrap gap-2 text-sm" data-testid="home-shared">
					{#each shared as j (j.id)}
						<li>
							<a
								class="rounded-md border px-3 py-1 hover:bg-muted"
								href={resolve('/jobs/[id]', { id: j.id })}
								data-testid={`home-shared-${j.name}`}>{j.name}</a
							>
						</li>
					{/each}
				</ul>
			</section>
		{/if}
	{/if}
</main>
