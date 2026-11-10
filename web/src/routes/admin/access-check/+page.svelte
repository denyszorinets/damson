<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { checkAccess, describeSource, splitGroups, type AccessCheck } from '$lib/access';
	import { problemMessage } from '$lib/jobs';

	let groups = $state('');
	let email = $state('');
	let project = $state('');
	let jobId = $state('');
	let result = $state<AccessCheck | null>(null);
	let checkError = $state<string | null>(null);
	let busy = $state(false);

	async function check(e: Event) {
		e.preventDefault();
		busy = true;
		checkError = null;
		result = null;
		try {
			result = await checkAccess({
				groups: splitGroups(groups),
				email: email.trim(),
				project: project.trim(),
				jobId: jobId.trim()
			});
		} catch (err) {
			checkError = problemMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<svelte:head>
	<title>Access check - Damson</title>
</svelte:head>

<div class="space-y-6">
	<h1 class="text-2xl font-semibold">Access check</h1>
	<section class="space-y-3" aria-label="Check access" data-testid="access-check">
		<h2 class="text-lg font-medium">Why can they do that?</h2>
		<p class="text-sm text-muted-foreground">
			Enter the groups (and optionally an email) of a person, and a project or job. The access rules
			are evaluated as they are when enforced. The check is recorded in the audit log without the
			groups or the email.
		</p>
		<form class="grid max-w-xl gap-3" onsubmit={check}>
			<Label for="check-groups">Groups (commas, spaces or lines)</Label>
			<Input id="check-groups" bind:value={groups} data-testid="check-groups" autocomplete="off" />
			<Label for="check-email">Email (optional)</Label>
			<Input id="check-email" bind:value={email} data-testid="check-email" autocomplete="off" />
			<Label for="check-project">Project</Label>
			<Input
				id="check-project"
				bind:value={project}
				data-testid="check-project"
				placeholder="acme/payments"
				autocomplete="off"
			/>
			<Label for="check-job">Or a job id</Label>
			<Input id="check-job" bind:value={jobId} data-testid="check-job" autocomplete="off" />
			<div><Button type="submit" disabled={busy} data-testid="check-run">Check</Button></div>
		</form>
		{#if checkError}
			<Alert.Root variant="destructive" data-testid="check-error">
				<Alert.Description>{checkError}</Alert.Description>
			</Alert.Root>
		{/if}
		{#if result}
			<div class="space-y-1 rounded-md border p-4 text-sm" data-testid="check-result">
				{#if result.role && result.source}
					<p>
						<strong data-testid="check-role">{result.role}</strong> on {result.project}, from
						<span data-testid="check-source">{describeSource(result.source)}</span>.
					</p>
					{#if result.source.rule}
						<p>
							Rule: <code class="font-mono" data-testid="check-rule">{result.source.rule}</code>
						</p>
					{/if}
				{:else}
					<p data-testid="check-none">
						No rule grants a role on {result.project} ({result.rules} evaluated).
					</p>
				{/if}
				<p class="text-muted-foreground">{result.note}</p>
			</div>
		{/if}
	</section>
</div>
