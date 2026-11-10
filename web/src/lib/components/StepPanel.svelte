<script lang="ts">
	import { Badge } from '$lib/components/ui/badge';
	import { problemMessage } from '$lib/jobs';
	import { fetchRunDetail, type StepDetail } from '$lib/runs';

	/**
	 * What one step did: its status, error and each attempt, with a button to open
	 * an attempt's log. It reads the run again when `stamp` changes (the step's
	 * status or attempt moved). Inputs and outputs are not shown here.
	 */
	let {
		runId,
		path,
		stamp,
		selected,
		onselect
	}: {
		runId: string;
		path: string;
		stamp: string;
		selected: string | null;
		onselect: (executionId: string) => void;
	} = $props();

	let step = $state<StepDetail | null>(null);
	let error = $state<string | null>(null);

	$effect(() => {
		void stamp;
		const ctl = new AbortController();
		fetchRunDetail(runId, ctl.signal)
			.then((r) => {
				step = (r.steps ?? []).find((s) => s.path === path) ?? null;
				error = null;
			})
			.catch((e) => {
				if (!ctl.signal.aborted) error = problemMessage(e);
			});
		return () => ctl.abort();
	});

	const time = (t?: string) => (t ? new Date(t).toLocaleTimeString() : '');
</script>

<section class="space-y-3 rounded-md border p-4" data-testid="step-panel" aria-label="Step detail">
	<div class="flex items-center gap-3">
		<h2 class="font-mono text-sm font-medium" data-testid="step-path">{path}</h2>
		{#if step}<Badge variant="secondary" data-testid="step-status">{step.status}</Badge>{/if}
		{#if step?.plugin}<span class="text-xs text-muted-foreground">{step.plugin}</span>{/if}
	</div>
	{#if error}
		<p class="text-sm text-destructive" role="alert">{error}</p>
	{:else if step}
		{#if step.error}
			<p class="text-sm text-destructive" data-testid="step-error">
				{step.error}{#if step.error_kind}
					<span class="text-muted-foreground"> ({step.error_kind})</span>{/if}
			</p>
		{/if}
		<table class="w-full text-left text-xs" data-testid="step-attempts">
			<thead class="text-muted-foreground">
				<tr
					><th>Attempt</th><th>State</th><th>Agent</th><th>Started</th><th>Finished</th><th>Log</th
					></tr
				>
			</thead>
			<tbody>
				{#each step.executions ?? [] as ex (ex.id)}
					<tr data-testid="attempt-row" data-attempt={ex.attempt}>
						<td>{ex.attempt}</td>
						<td
							>{ex.state}{#if ex.error_kind}
								({ex.error_kind}){/if}</td
						>
						<td class="font-mono">{ex.agent_id ?? ''}</td>
						<td>{time(ex.started_at)}</td>
						<td>{time(ex.finished_at)}</td>
						<td>
							<button
								type="button"
								class="underline underline-offset-4"
								aria-pressed={selected === ex.id}
								onclick={() => onselect(ex.id)}
								data-testid={`attempt-log-${ex.attempt}`}>Log</button
							>
						</td>
					</tr>
				{:else}
					<tr><td colspan="6" class="text-muted-foreground">No attempts yet.</td></tr>
				{/each}
			</tbody>
		</table>
	{/if}
</section>
