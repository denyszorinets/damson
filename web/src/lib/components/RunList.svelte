<script lang="ts">
	import { resolve } from '$app/paths';
	import { Badge } from '$lib/components/ui/badge';
	import type { Job } from '$lib/jobs';
	import type { RunSummary } from '$lib/runs';

	let { runs, jobs }: { runs: RunSummary[]; jobs: Map<string, Job> } = $props();

	const when = (r: RunSummary) => new Date(r.created_at).toLocaleString();
</script>

<ul class="divide-y rounded-md border text-sm" data-testid="run-list">
	{#each runs as r (r.id)}
		<li class="flex items-center gap-3 px-3 py-2" data-testid="run-row">
			<Badge
				variant={r.status === 'failed' ? 'destructive' : 'secondary'}
				data-testid="run-row-status">{r.status}</Badge
			>
			<a class="flex-1 truncate hover:underline" href={resolve('/runs/[id]', { id: r.id })}
				>{jobs.get(r.job_id)?.name ?? r.job_id}</a
			>
			<span class="text-xs text-muted-foreground">{when(r)}</span>
		</li>
	{/each}
</ul>
