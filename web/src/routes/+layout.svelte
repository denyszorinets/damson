<script lang="ts">
	import './layout.css';
	import { resolve } from '$app/paths';
	import { Button } from '$lib/components/ui/button';
	import { logout } from '$lib/session';
	import { page } from '$app/state';
	import ProjectSwitcher from '$lib/components/ProjectSwitcher.svelte';
	import { breadcrumb, parseProjectRoute, projectRest } from '$lib/projectroute';
	import type { LayoutProps } from './$types';

	let { data, children }: LayoutProps = $props();
	const me = $derived(data.me);
	// A logged-in user who is not an instance admin has no use for the admin pages.
	const admin = $derived(!me || me.kind !== 'user' || me.instance_admin);
	// The project the URL is inside, if any: it drives the switcher and the breadcrumb.
	const project = $derived(
		page.url.pathname.startsWith('/projects/') ? parseProjectRoute(page.params.path).path : ''
	);
	const crumbs = $derived(breadcrumb(project));
</script>

<nav class="border-b" aria-label="Main">
	<div class="mx-auto flex max-w-6xl items-center gap-6 px-4 py-3 text-sm">
		<a href={resolve('/')} class="font-semibold">Damson</a>
		{#if me?.kind === 'user'}
			<ProjectSwitcher current={project} />
		{/if}
		<a href={resolve('/projects')} class="text-muted-foreground hover:text-foreground">Projects</a>
		<a href={resolve('/agents')} class="text-muted-foreground hover:text-foreground">Agents</a>
		{#if me?.kind === 'user'}
			<a href={resolve('/access')} class="text-muted-foreground hover:text-foreground">Access</a>
		{/if}
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
{#if crumbs.length}
	<nav class="border-b" aria-label="Breadcrumb" data-testid="breadcrumb">
		<ol
			class="mx-auto flex max-w-6xl flex-wrap items-center gap-1 px-4 py-2 text-sm text-muted-foreground"
		>
			<li><a class="hover:text-foreground" href={resolve('/')}>Home</a></li>
			{#each crumbs as c (c.path)}
				<li aria-hidden="true">/</li>
				<li>
					<a
						class="hover:text-foreground"
						href={resolve('/projects/[...path]', { path: projectRest(c.path) })}
						data-testid="crumb-{c.path}">{c.label}</a
					>
				</li>
			{/each}
		</ol>
	</nav>
{/if}

{#if data.loginError}
	<main class="mx-auto max-w-6xl px-4 py-6">
		<p class="text-destructive" role="alert" data-testid="login-error">{data.loginError}</p>
	</main>
{:else}
	{@render children()}
{/if}
