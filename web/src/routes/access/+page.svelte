<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Badge } from '$lib/components/ui/badge';
	import { Skeleton } from '$lib/components/ui/skeleton';
	import { describeSource, fetchMyAccess, type MyAccessItem } from '$lib/access';
	import { problemMessage } from '$lib/jobs';
	import { isInstanceAdmin } from '$lib/projects';
	import { resolve } from '$app/paths';

	let items = $state<MyAccessItem[] | null>(null);
	let error = $state<string | null>(null);

	$effect(() => {
		const ctl = new AbortController();
		fetchMyAccess(ctl.signal)
			.then((r) => (items = r))
			.catch((e) => {
				if (!ctl.signal.aborted) error = problemMessage(e);
			});
		return () => ctl.abort();
	});
</script>

<svelte:head>
	<title>Access - Damson</title>
</svelte:head>

<main class="mx-auto max-w-5xl space-y-10 px-4 py-8">
	<h1 class="text-2xl font-semibold">My access</h1>

	<section class="space-y-3" aria-label="My access">
		<h2 class="text-lg font-medium">My access</h2>
		{#if error}
			<Alert.Root variant="destructive" data-testid="access-error">
				<Alert.Description>{error}</Alert.Description>
			</Alert.Root>
		{:else if items === null}
			<Skeleton class="h-24 w-full" />
		{:else if !items.length}
			<p class="text-sm text-muted-foreground" data-testid="access-none">
				You hold no roles. Ask an administrator for access.
			</p>
		{:else}
			<Table.Root data-testid="my-access">
				<Table.Header>
					<Table.Row>
						<Table.Head>Where</Table.Head>
						<Table.Head>Role</Table.Head>
						<Table.Head>Granted by</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each items as it (it.project ?? it.job_id)}
						<Table.Row data-testid="my-access-row" data-where={it.project ?? it.job_id}>
							<Table.Cell>
								{#if it.project}{it.project}{:else}job {it.name}{/if}
							</Table.Cell>
							<Table.Cell><Badge variant="outline">{it.role}</Badge></Table.Cell>
							<Table.Cell data-testid="my-access-source">{describeSource(it.source)}</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		{/if}
	</section>

	{#if isInstanceAdmin()}
		<p class="text-sm text-muted-foreground">
			Instance admins can check another person's access under
			<a class="underline underline-offset-4" href={resolve('/admin/access-check')}>Admin</a>.
		</p>
	{/if}
</main>
