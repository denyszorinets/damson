<script lang="ts">
	import { page } from '$app/state';
	import JobView from '$lib/components/JobView.svelte';
	import JobsView from '$lib/components/JobsView.svelte';
	import NotFound from '$lib/components/NotFound.svelte';
	import ProjectAccess from '$lib/components/ProjectAccess.svelte';
	import ProjectActivity from '$lib/components/ProjectActivity.svelte';
	import ProjectNav from '$lib/components/ProjectNav.svelte';
	import ProjectOverview from '$lib/components/ProjectOverview.svelte';
	import ProjectSettings from '$lib/components/ProjectSettings.svelte';
	import ProjectTable from '$lib/components/ProjectTable.svelte';
	import RunView from '$lib/components/RunView.svelte';
	import { atLeast, fetchProject } from '$lib/projects';
	import { parseProjectRoute } from '$lib/projectroute';
	import { isUser } from '$lib/session';

	// The URL is the project's path, then "/-/" and a tab; a view is stable and shareable.
	const route = $derived(parseProjectRoute(page.params.path));
	// The front page is the jobs table; the other tabs are named after the "-". A job is
	// jobs/<id>[/<tab>] and a run runs/<id>; the sidebar highlights Jobs and Activity for them.
	const head = $derived(route.tab.length ? route.tab[0] : '');
	const isJob = $derived(head === 'jobs' && route.tab.length >= 2 && route.tab.length <= 3);
	const isRun = $derived(head === 'runs' && route.tab.length === 2);
	const tab = $derived(isJob ? '' : isRun ? 'activity' : head);
	const known = $derived(
		isJob ||
			isRun ||
			(route.tab.length <= 1 && ['', 'overview', 'activity', 'access', 'settings'].includes(head))
	);
	// Settings and Access are for the project's admins: the role decides which tabs show, and
	// anyone else who types the URL gets the not-found view, as for a project they cannot see.
	const adminTab = $derived(head === 'access' || head === 'settings');

	// The caller's role on this project; unknown until read, and absent without a login.
	let role = $state<string | undefined>(undefined);
	let roleLoaded = $state(false);
	$effect(() => {
		const current = route.path;
		role = undefined;
		roleLoaded = false;
		if (!isUser()) {
			roleLoaded = true;
			return;
		}
		void fetchProject(current).then(
			(p) => {
				if (current !== route.path) return;
				role = p?.my_role;
				roleLoaded = true;
			},
			() => {
				if (current === route.path) roleLoaded = true;
			}
		);
	});
	const allowed = $derived(!adminTab || (roleLoaded && atLeast(role, 'admin')));
</script>

<svelte:head>
	<title>{route.path} - Damson</title>
</svelte:head>

<main class="mx-auto flex max-w-6xl flex-col gap-6 px-4 py-8 md:flex-row">
	{#if known && (allowed || (adminTab && !roleLoaded))}
		<aside class="shrink-0 md:w-48">
			<ProjectNav path={route.path} {tab} {role} />
		</aside>
		<div class="min-w-0 flex-1">
			{#key route.path}
				{#if !allowed}
					<!-- the role is still being read -->
				{:else if isJob}
					<JobView path={route.path} id={route.tab[1]} sub={route.tab.slice(2)} />
				{:else if isRun}
					<RunView path={route.path} id={route.tab[1]} />
				{:else if head === 'overview'}
					<ProjectOverview path={route.path} />
				{:else if head === 'activity'}
					<ProjectActivity path={route.path} />
				{:else if head === 'access'}
					<ProjectAccess path={route.path} />
				{:else if head === 'settings'}
					<ProjectSettings path={route.path} />
				{:else if isUser()}
					<ProjectTable path={route.path} />
				{:else}
					<!-- A bearer token has no session for the tree: the flat list of the project and below. -->
					<JobsView scope={route.path} />
				{/if}
			{/key}
		</div>
	{:else if known}
		<aside class="shrink-0 md:w-48">
			<ProjectNav path={route.path} {tab} {role} />
		</aside>
		<div class="min-w-0 flex-1"><NotFound /></div>
	{:else}
		<div class="min-w-0 flex-1"><NotFound /></div>
	{/if}
</main>
