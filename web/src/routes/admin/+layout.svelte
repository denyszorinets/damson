<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import NotFound from '$lib/components/NotFound.svelte';
	import { isInstanceAdmin } from '$lib/projects';
	import type { LayoutProps } from './$types';

	/**
	 * The instance-wide tools, for instance admins only (and bearer-token operators,
	 * who have no session role). Anyone else gets the not-found view, as for a page
	 * that does not exist; the server checks every call again.
	 */
	let { children }: LayoutProps = $props();

	const items = [
		{ href: '/admin/agents', label: 'Agents', id: 'agents' },
		{ href: '/admin/audit', label: 'Audit', id: 'audit' },
		{ href: '/admin/nodes', label: 'Nodes', id: 'nodes' },
		{ href: '/admin/secrets', label: 'Secrets', id: 'secrets' },
		{ href: '/admin/service-accounts', label: 'Service accounts', id: 'service-accounts' },
		{ href: '/admin/access-check', label: 'Access check', id: 'access-check' },
		{ href: '/admin/log-purge', label: 'Log purge', id: 'log-purge' }
	] as const;
	const here = $derived(page.url.pathname);
</script>

{#if !isInstanceAdmin()}
	<main class="mx-auto max-w-6xl px-4 py-8"><NotFound /></main>
{:else}
	<div class="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-6 md:flex-row">
		<aside class="shrink-0 md:w-48">
			<nav aria-label="Admin" class="flex gap-1 md:flex-col" data-testid="admin-nav">
				<h2 class="hidden px-3 py-1 text-xs font-medium text-muted-foreground uppercase md:block">
					Admin
				</h2>
				{#each items as item (item.id)}
					<a
						class="rounded px-3 py-1.5 text-sm hover:bg-muted"
						class:bg-muted={here === item.href || here.startsWith(item.href + '/')}
						class:font-medium={here === item.href || here.startsWith(item.href + '/')}
						aria-current={here === item.href ? 'page' : undefined}
						href={resolve(item.href)}
						data-testid="admin-nav-{item.id}">{item.label}</a
					>
				{/each}
			</nav>
		</aside>
		<div class="min-w-0 flex-1">{@render children()}</div>
	</div>
{/if}
