<script lang="ts">
	import { SESSION_TOKEN } from '$lib/session';
	import { onMount } from 'svelte';
	import * as Alert from '$lib/components/ui/alert';
	import * as Table from '$lib/components/ui/table';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import TokenGate from '$lib/components/TokenGate.svelte';
	import {
		Unauthorized,
		createServiceAccount,
		createServiceToken,
		deleteServiceAccount,
		fetchServiceAccounts,
		fetchServiceTokens,
		loadToken,
		problemMessage,
		revokeServiceToken,
		saveToken,
		type ServiceAccount,
		type ServiceToken
	} from '$lib/triggers';

	let token = $state('');
	let accounts = $state<ServiceAccount[]>([]);
	let selected = $state<ServiceAccount | null>(null);
	let tokens = $state<ServiceToken[]>([]);
	let error = $state<string | null>(null);
	let busy = $state(false);
	// The secret of a token just created: shown once, then dropped.
	let newSecret = $state<string | null>(null);

	let accName = $state('');
	let tokName = $state('');
	let tokProject = $state('default');
	let tokRole = $state<'viewer' | 'runner' | 'editor'>('runner');

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
		accounts = tokens = [];
		selected = null;
		newSecret = null;
	}

	async function load() {
		if (!token) return;
		try {
			accounts = await fetchServiceAccounts(token);
			tokens = selected ? await fetchServiceTokens(token, selected.id) : [];
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

	async function select(a: ServiceAccount) {
		selected = a;
		newSecret = null;
		await load();
	}

	onMount(() => {
		token = loadToken();
		void load();
	});
</script>

<svelte:head>
	<title>Service accounts - Damson</title>
</svelte:head>

<main class="mx-auto max-w-6xl space-y-6 px-4 py-8">
	<div class="flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Service accounts</h1>
		{#if token}
			{#if token !== SESSION_TOKEN}
				<Button variant="outline" data-testid="forget-token" onclick={forget}>Forget token</Button>
			{/if}
		{/if}
	</div>

	{#if error}
		<Alert.Root variant="destructive" data-testid="sa-error">
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
		<Table.Root data-testid="accounts">
			<Table.Header>
				<Table.Row>
					<Table.Head>Name</Table.Head>
					<Table.Head>Description</Table.Head>
					<Table.Head></Table.Head>
				</Table.Row>
			</Table.Header>
			<Table.Body>
				{#each accounts as a (a.id)}
					<Table.Row data-testid="account-row">
						<Table.Cell>
							<button class="underline" onclick={() => select(a)}>{a.name}</button>
						</Table.Cell>
						<Table.Cell>{a.description ?? ''}</Table.Cell>
						<Table.Cell class="text-right">
							<Button
								variant="outline"
								size="sm"
								disabled={busy}
								onclick={() =>
									act(async () => {
										await deleteServiceAccount(token, a.id);
										if (selected?.id === a.id) selected = null;
									})}>Delete</Button
							>
						</Table.Cell>
					</Table.Row>
				{:else}
					<Table.Row
						><Table.Cell colspan={3} class="text-muted-foreground">No service accounts.</Table.Cell
						></Table.Row
					>
				{/each}
			</Table.Body>
		</Table.Root>
		<form
			class="grid max-w-md gap-2"
			data-testid="account-form"
			onsubmit={(e) => {
				e.preventDefault();
				void act(async () => {
					await createServiceAccount(token, accName.trim(), '');
					accName = '';
				});
			}}
		>
			<Label for="acc-name">New account (lower-case name)</Label>
			<Input id="acc-name" bind:value={accName} required />
			<Button type="submit" disabled={busy}>Create account</Button>
		</form>

		{#if selected}
			<section class="space-y-3" data-testid="tokens-section">
				<h2 class="text-lg font-medium">Tokens of {selected.name}</h2>
				{#if newSecret}
					<Alert.Root data-testid="new-token">
						<Alert.Description>
							Copy this token now; it is not shown again:
							<code class="break-all" data-testid="new-token-secret">{newSecret}</code>
							<Button variant="outline" size="sm" onclick={() => (newSecret = null)}>Done</Button>
						</Alert.Description>
					</Alert.Root>
				{/if}
				<Table.Root>
					<Table.Header>
						<Table.Row>
							<Table.Head>Name</Table.Head>
							<Table.Head>Scope</Table.Head>
							<Table.Head>Role</Table.Head>
							<Table.Head>Last used</Table.Head>
							<Table.Head></Table.Head>
						</Table.Row>
					</Table.Header>
					<Table.Body>
						{#each tokens as t (t.id)}
							<Table.Row data-testid="token-row">
								<Table.Cell>{t.name}{t.revoked ? ' (revoked)' : ''}</Table.Cell>
								<Table.Cell>{t.project || 'all projects'}</Table.Cell>
								<Table.Cell>{t.role}</Table.Cell>
								<Table.Cell>{t.last_used_at ?? '-'}</Table.Cell>
								<Table.Cell class="text-right">
									{#if !t.revoked}
										<Button
											variant="outline"
											size="sm"
											disabled={busy}
											onclick={() => act(() => revokeServiceToken(token, t.id))}>Revoke</Button
										>
									{/if}
								</Table.Cell>
							</Table.Row>
						{:else}
							<Table.Row
								><Table.Cell colspan={5} class="text-muted-foreground">No tokens.</Table.Cell
								></Table.Row
							>
						{/each}
					</Table.Body>
				</Table.Root>
				<form
					class="grid max-w-md gap-2"
					data-testid="token-create-form"
					onsubmit={(e) => {
						e.preventDefault();
						const account = selected;
						if (!account) return;
						void act(async () => {
							const created = await createServiceToken(token, account.id, {
								name: tokName.trim(),
								project: tokProject.trim(),
								role: tokRole
							});
							newSecret = created.token;
							tokName = '';
						});
					}}
				>
					<Label for="tok-name">Token name</Label>
					<Input id="tok-name" bind:value={tokName} required />
					<Label for="tok-project">Project (empty: all projects)</Label>
					<Input id="tok-project" bind:value={tokProject} />
					<Label for="tok-role">Role</Label>
					<select id="tok-role" class="rounded-md border px-2 py-1" bind:value={tokRole}>
						<option value="viewer">viewer</option>
						<option value="runner">runner</option>
						<option value="editor">editor</option>
					</select>
					<Button type="submit" disabled={busy}>Create token</Button>
				</form>
			</section>
		{/if}
	{/if}
</main>
