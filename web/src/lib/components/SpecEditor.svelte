<script lang="ts">
	import * as Alert from '$lib/components/ui/alert';
	import { Button } from '$lib/components/ui/button';
	import GraphView from '$lib/graph/GraphView.svelte';
	import JobTriggers from '$lib/components/JobTriggers.svelte';
	import SpecDetails from '$lib/components/SpecDetails.svelte';
	import SpecOptions from '$lib/components/SpecOptions.svelte';
	import SpecSteps from '$lib/components/SpecSteps.svelte';
	import { problemMessage } from '$lib/jobs';
	import { countByTab, readSpec, toText, type Spec } from '$lib/specmodel';
	import { arrowNav } from '$lib/keys';
	import { SpecRejected, validateSpec, type SpecIssue, type Validation } from '$lib/specedit';

	/**
	 * Edits a job spec in tabs (Details, Options, Steps, Triggers) over the JSON
	 * text, which stays the one source of truth and has its own tab. Checking is on demand: the server validates and
	 * answers with errors by path and, when the spec is valid, the graph it built,
	 * which is drawn beside the editor. The page decides what Save does.
	 */
	let {
		text = $bindable(),
		readonly = false,
		saveLabel,
		onsave,
		jobId,
		role
	}: {
		text: string;
		readonly?: boolean;
		saveLabel: string;
		onsave: (text: string) => Promise<void>;
		/** The job being edited; the Triggers tab shows only for an existing job. */
		jobId?: string;
		role?: string;
	} = $props();

	type Tab = 'details' | 'options' | 'steps' | 'triggers' | 'json';
	let tab = $state<Tab>('details');
	const spec = $derived(readSpec(text));
	const tabs = $derived<{ id: Tab; label: string }[]>([
		{ id: 'details', label: 'Details' },
		{ id: 'options', label: 'Options' },
		{ id: 'steps', label: 'Steps' },
		...(jobId ? [{ id: 'triggers' as const, label: 'Triggers' }] : []),
		{ id: 'json', label: 'JSON' }
	]);

	function change(next: Spec) {
		text = toText(next);
		checked = null;
	}

	let checked = $state<Validation | null>(null);
	let issues = $state<SpecIssue[]>([]);
	let failure = $state<string | null>(null);
	let busy = $state(false);
	const counts = $derived(countByTab(issues));

	async function check() {
		busy = true;
		failure = null;
		try {
			checked = await validateSpec(text);
			issues = checked.errors;
		} catch (e) {
			checked = null;
			if (e instanceof SpecRejected) issues = e.issues;
			else failure = problemMessage(e);
		} finally {
			busy = false;
		}
	}

	async function save() {
		busy = true;
		failure = null;
		try {
			await onsave(text);
		} catch (e) {
			if (e instanceof SpecRejected) {
				issues = e.issues;
				checked = null;
			} else failure = problemMessage(e);
			busy = false;
		}
	}
</script>

<div class="grid gap-6 md:grid-cols-2" data-testid="spec-editor">
	<div class="space-y-3">
		<nav aria-label="Editor" class="flex flex-wrap gap-1 border-b" use:arrowNav>
			{#each tabs as t (t.id)}
				{@const n = t.id === 'triggers' ? 0 : counts[t.id]}
				<button
					type="button"
					class="rounded-t px-3 py-1.5 text-sm hover:bg-muted"
					class:bg-muted={tab === t.id}
					class:font-medium={tab === t.id}
					aria-current={tab === t.id ? 'page' : undefined}
					onclick={() => (tab = t.id)}
					data-arrow
					data-testid="spec-tab-{t.id}"
					>{t.label}{#if n}<span class="ml-1 text-destructive" data-testid="spec-tab-errors-{t.id}"
							>({n})</span
						>{/if}</button
				>
			{/each}
		</nav>
		{#if tab === 'json'}
			<label class="text-sm font-medium" for="spec-text">Job spec (JSON)</label>
			<textarea
				id="spec-text"
				class="h-96 w-full rounded-md border bg-background p-3 font-mono text-xs"
				spellcheck="false"
				{readonly}
				bind:value={text}
				oninput={() => (checked = null)}
				data-testid="spec-text"></textarea>
		{:else if tab === 'triggers' && jobId}
			<p class="text-xs text-muted-foreground">
				Schedules and webhooks are saved on their own; they are not part of a version.
			</p>
			<JobTriggers {jobId} {role} />
		{:else if !spec}
			<p class="text-sm text-destructive" role="alert" data-testid="spec-unparsed">
				The JSON does not parse; fix it in the JSON tab to use this tab.
			</p>
		{:else if tab === 'details'}
			<SpecDetails {spec} {readonly} onchange={change} />
		{:else if tab === 'options'}
			<SpecOptions {spec} {readonly} onchange={change} />
		{:else if tab === 'steps'}
			<SpecSteps {spec} {readonly} onchange={change} />
		{/if}
		{#if !readonly}
			<div class="flex gap-2">
				<Button variant="outline" disabled={busy} onclick={check} data-testid="spec-validate"
					>Validate</Button
				>
				<Button disabled={busy} onclick={save} data-testid="spec-save">{saveLabel}</Button>
			</div>
		{/if}
		{#if failure}
			<Alert.Root variant="destructive" data-testid="spec-failure">
				<Alert.Description>{failure}</Alert.Description>
			</Alert.Root>
		{/if}
		{#if issues.length}
			<ul class="space-y-1 text-sm text-destructive" role="alert" data-testid="spec-issues">
				{#each issues as i, n (n)}
					<li data-testid="spec-issue">
						<code class="font-mono">{i.path}</code>: {i.message}
					</li>
				{/each}
			</ul>
		{:else if checked?.valid}
			<p class="text-sm text-green-700 dark:text-green-400" data-testid="spec-valid">
				The spec is valid.
			</p>
		{/if}
		{#if checked?.warnings.length}
			<ul class="space-y-1 text-sm text-amber-700 dark:text-amber-400" data-testid="spec-warnings">
				{#each checked.warnings as w, n (n)}
					<li><code class="font-mono">{w.path}</code>: {w.message}</li>
				{/each}
			</ul>
		{/if}
	</div>
	<div class="space-y-2">
		<h2 class="text-sm font-medium">Graph</h2>
		{#if checked?.graph}
			{#key checked.graph}
				<GraphView graph={checked.graph} />
			{/key}
		{:else}
			<p class="text-sm text-muted-foreground" data-testid="spec-no-graph">
				Validate the spec to see the graph the server builds from it.
			</p>
		{/if}
	</div>
</div>
