<script lang="ts">
	import { resolve } from '$app/paths';

	/**
	 * Shown when the server answers 404. A target the caller has no role on reads
	 * the same as one that does not exist, so the page says neither.
	 */
	let { what = 'This page' }: { what?: string } = $props();
</script>

<section class="space-y-3" data-testid="not-found">
	<h2 class="text-lg font-medium">{what} was not found</h2>
	<p class="text-muted-foreground">It may not exist, or you may not have access to it.</p>
	<p class="flex gap-4 text-sm">
		<a class="underline underline-offset-4" href={resolve('/projects')}>Projects</a>
		<a class="underline underline-offset-4" href={resolve('/')}>Home</a>
	</p>
</section>
