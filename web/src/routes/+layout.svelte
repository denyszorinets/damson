<script lang="ts">
	import './layout.css';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { logout } from '$lib/session';
	import { page } from '$app/state';
	import ProjectTree from '$lib/components/ProjectTree.svelte';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
	const me = $derived(data.me);
	// A logged-in user who is not an instance admin has no use for the admin pages.
	const admin = $derived(!me || me.kind !== 'user' || me.instance_admin);
	// The tree is the signed-in user's own view; a bearer token has no session to ask with.
	const treeSub = $derived(me?.kind === 'user' ? me.sub : '');
	const currentJob = $derived(
		page.params.id && page.url.pathname.startsWith('/jobs/') ? page.params.id : ''
	);
</script>

<nav class="border-b" aria-label="Main">
	<div class="mx-auto flex max-w-6xl items-center gap-6 px-4 py-3 text-sm">
		<a href={resolve('/')} class="font-semibold">Damson</a>
		<a href={resolve('/jobs')} class="text-muted-foreground hover:text-foreground">Jobs</a>
		<a href={resolve('/projects')} class="text-muted-foreground hover:text-foreground">Projects</a>
		<a href={resolve('/agents')} class="text-muted-foreground hover:text-foreground">Agents</a>
		{#if admin}
			<a href={resolve('/nodes')} class="text-muted-foreground hover:text-foreground">Nodes</a>
			<a href={resolve('/secrets')} class="text-muted-foreground hover:text-foreground">Secrets</a>
			<a href={resolve('/service-accounts')} class="text-muted-foreground hover:text-foreground"
				>Accounts</a
			>
			<a href={resolve('/audit')} class="text-muted-foreground hover:text-foreground">Audit</a>
		{/if}
		{#if me?.kind === 'user'}
			<span class="ml-auto flex items-center gap-3" data-testid="user-menu">
				<span class="text-muted-foreground" data-testid="user-name">{me.email || me.sub}</span>
				<Button variant="outline" size="sm" onclick={logout} data-testid="logout">Log out</Button>
			</span>
		{/if}
	</div>
</nav>

{#if data.loginError}
	<main class="mx-auto max-w-6xl px-4 py-6">
		<p class="text-destructive" role="alert" data-testid="login-error">{data.loginError}</p>
	</main>
{:else}
	{#if treeSub}
		<div class="mx-auto flex max-w-6xl gap-6 px-4">
			<aside class="hidden w-60 shrink-0 py-6 md:block" aria-label="Navigation">
				{#key treeSub}
					<ProjectTree sub={treeSub} {currentJob} />
				{/key}
			</aside>
			<div class="min-w-0 flex-1">{@render children()}</div>
		</div>
	{:else}
		{@render children()}
	{/if}
{/if}
