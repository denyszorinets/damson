<script lang="ts">
	import './layout.css';
	import { resolve } from '$app/paths';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();
</script>

<nav class="border-b" aria-label="Main">
	<div class="mx-auto flex max-w-6xl items-center gap-6 px-4 py-3 text-sm">
		<a href={resolve('/')} class="font-semibold">Damson</a>
		<a href={resolve('/agents')} class="text-muted-foreground hover:text-foreground">Agents</a>
	</div>
</nav>

{@render children()}
