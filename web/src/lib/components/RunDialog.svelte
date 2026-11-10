<script lang="ts">
	// The run dialog of a job: a form generated from spec.options (ARCHITECTURE
	// 12.3), or a plain confirmation when the job takes no options.
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import SchemaForm from '$lib/components/SchemaForm.svelte';
	import { hasFields, problemMessage, triggerRun } from '$lib/triggers';
	import { atLeast } from '$lib/projects';

	let {
		jobId,
		schema,
		role
	}: { jobId: string; schema: Record<string, unknown> | null; role?: string } = $props();
	const canRun = $derived(atLeast(role, 'runner'));

	let open = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);

	async function start(options: Record<string, unknown>) {
		busy = true;
		error = null;
		try {
			const id = await triggerRun(jobId, options);
			await goto(resolve('/runs/[id]', { id }));
		} catch (e) {
			error = problemMessage(e);
			busy = false;
		}
	}
</script>

<div class="space-y-3">
	<div class="flex items-center gap-3">
		<Button
			data-testid="run-job"
			disabled={!canRun}
			title={canRun ? undefined : 'Running a job needs the runner role'}
			onclick={() => (open = !open)}>Run…</Button
		>
		{#if !canRun}
			<span class="text-sm text-muted-foreground" data-testid="run-needs">needs runner</span>
		{/if}
	</div>
	{#if open && canRun}
		<section class="space-y-3 rounded-md border p-4" data-testid="run-dialog" aria-label="Run job">
			<h2 class="text-lg font-medium">Run this job</h2>
			{#if error}
				<Alert.Root variant="destructive" data-testid="run-error">
					<Alert.Description>{error}</Alert.Description>
				</Alert.Root>
			{/if}
			{#if schema && hasFields(schema)}
				<SchemaForm {schema} onsubmit={start} />
			{:else}
				<p class="text-sm text-muted-foreground">This job takes no options.</p>
				<Button data-testid="run-confirm" disabled={busy} onclick={() => start({})}
					>Start run</Button
				>
			{/if}
		</section>
	{/if}
</div>
