<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { problemMessage } from '$lib/jobs';
	import { purgeExecutionLogs, type PurgeResult } from '$lib/purge';

	let executionId = $state('');
	let confirming = $state(false);
	let busy = $state(false);
	let error = $state<string | null>(null);
	let result = $state<PurgeResult | null>(null);

	async function purge() {
		busy = true;
		error = null;
		result = null;
		try {
			result = await purgeExecutionLogs(executionId.trim());
			executionId = '';
		} catch (e) {
			error = problemMessage(e);
		} finally {
			busy = false;
			confirming = false;
		}
	}
</script>

<svelte:head>
	<title>Log purge - Damson</title>
</svelte:head>

<div class="space-y-6">
	<h1 class="text-2xl font-semibold">Log purge</h1>
	<p class="max-w-xl text-sm text-muted-foreground">
		Removes every stored log chunk of one finished execution, for a leak the masker missed. The
		purge is recorded in the audit log. An execution that can still write logs must be cancelled
		first.
	</p>
	<form
		class="grid max-w-xl gap-3"
		data-testid="purge-form"
		onsubmit={(e) => {
			e.preventDefault();
			confirming = true;
		}}
	>
		<Label for="purge-id">Execution id</Label>
		<Input
			id="purge-id"
			bind:value={executionId}
			required
			autocomplete="off"
			data-testid="purge-id"
		/>
		{#if !confirming}
			<div>
				<Button type="submit" disabled={busy || !executionId.trim()} data-testid="purge-start"
					>Purge logs</Button
				>
			</div>
		{:else}
			<div class="flex items-center gap-3">
				<span class="text-sm">Remove all logs of {executionId.trim()}?</span>
				<Button
					type="button"
					variant="destructive"
					onclick={purge}
					disabled={busy}
					data-testid="purge-confirm">Yes, purge</Button
				>
				<Button type="button" variant="outline" onclick={() => (confirming = false)}>Cancel</Button>
			</div>
		{/if}
	</form>
	{#if error}
		<Alert.Root variant="destructive" data-testid="purge-error">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{/if}
	{#if result}
		<Alert.Root data-testid="purge-result">
			<Alert.Description>
				Removed {result.chunks} log chunks and {result.objects} objects.
			</Alert.Description>
		</Alert.Root>
	{/if}
</div>
