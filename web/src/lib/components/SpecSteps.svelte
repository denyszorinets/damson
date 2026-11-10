<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { fetchPluginChoices, type PluginChoice } from '$lib/plugins';
	import {
		addStep,
		moveStep,
		removeStep,
		stepRows,
		updateStep,
		type Spec,
		type StepPatch
	} from '$lib/specmodel';

	/**
	 * The Steps tab: plugin steps as fields. `with` is JSON because its values may
	 * be ${{ }} expressions that a typed plugin form would refuse. A group, or a step
	 * with fields the form does not show, is edited whole as JSON; nothing is dropped.
	 */
	let {
		spec,
		readonly,
		onchange
	}: { spec: Spec; readonly: boolean; onchange: (next: Spec) => void } = $props();

	const rows = $derived(stepRows(spec));
	let plugins = $state<PluginChoice[]>([]);
	// Parse errors of the JSON boxes, by step index and box.
	let bad = $state<Record<string, string>>({});

	onMount(() => {
		const ctl = new AbortController();
		fetchPluginChoices(ctl.signal).then(
			(p) => (plugins = p),
			() => {}
		);
		return () => ctl.abort();
	});

	const edit = (i: number, patch: StepPatch) => onchange(updateStep(spec, i, patch));

	function json(i: number, box: 'with' | 'whole', text: string) {
		const key = `${box}-${i}`;
		try {
			const v: unknown = text.trim() === '' ? undefined : JSON.parse(text);
			if (v !== undefined && (typeof v !== 'object' || v === null || Array.isArray(v)))
				throw new Error('must be a JSON object');
			if (box === 'whole' && v === undefined) throw new Error('must be a JSON object');
			delete bad[key];
			edit(i, box === 'with' ? { with: v as Record<string, unknown> } : { whole: v as never });
		} catch (e) {
			bad[key] = e instanceof Error ? e.message : 'not valid JSON';
		}
	}

	const hint = (name: string) => plugins.find((p) => p.name === name.split('@')[0]);
	const pretty = (v: unknown) => (v === undefined ? '' : JSON.stringify(v, null, 2));
</script>

<div class="space-y-4" data-testid="spec-steps">
	<datalist id="plugin-names">
		{#each plugins as p (p.name)}<option value={p.name}>{p.title}</option>{/each}
	</datalist>
	{#each rows as r, i (i)}
		<section
			class="space-y-3 rounded-md border p-3"
			data-testid="step-row"
			aria-label="Step {i + 1}"
		>
			<div class="flex flex-wrap items-end gap-2">
				<div class="space-y-1">
					<label class="text-xs text-muted-foreground" for="s-id-{i}">Id</label>
					<Input
						id="s-id-{i}"
						class="w-36 font-mono"
						value={r.id}
						disabled={readonly}
						oninput={(e) => edit(i, { id: e.currentTarget.value })}
						data-testid="step-id-{i}"
					/>
				</div>
				{#if r.raw.type === 'group'}
					<span class="pb-2 text-sm text-muted-foreground">group</span>
				{:else}
					<div class="space-y-1">
						<label class="text-xs text-muted-foreground" for="s-plugin-{i}">Plugin</label>
						<Input
							id="s-plugin-{i}"
							class="w-40"
							list="plugin-names"
							value={r.plugin}
							disabled={readonly}
							oninput={(e) => edit(i, { plugin: e.currentTarget.value })}
							data-testid="step-plugin-{i}"
						/>
					</div>
				{/if}
				{#if !readonly}
					<div class="ml-auto flex gap-1">
						<Button
							variant="outline"
							size="sm"
							disabled={i === 0}
							aria-label="Move step {i + 1} up"
							onclick={() => onchange(moveStep(spec, i, -1))}>Up</Button
						>
						<Button
							variant="outline"
							size="sm"
							disabled={i === rows.length - 1}
							aria-label="Move step {i + 1} down"
							onclick={() => onchange(moveStep(spec, i, 1))}>Down</Button
						>
						<Button
							variant="outline"
							size="sm"
							aria-label="Remove step {i + 1}"
							onclick={() => onchange(removeStep(spec, i))}>Remove</Button
						>
					</div>
				{/if}
			</div>
			{#if r.raw.type !== 'group'}
				{#if hint(r.plugin)?.required.length}
					<p class="text-xs text-muted-foreground">
						{r.plugin} needs: {hint(r.plugin)?.required.join(', ')}
					</p>
				{/if}
				<div class="flex flex-wrap gap-2">
					<div class="space-y-1">
						<label class="text-xs text-muted-foreground" for="s-timeout-{i}">Timeout</label>
						<Input
							id="s-timeout-{i}"
							class="w-24"
							value={r.timeout}
							disabled={readonly}
							oninput={(e) => edit(i, { timeout: e.currentTarget.value })}
						/>
					</div>
					<div class="min-w-40 flex-1 space-y-1">
						<label class="text-xs text-muted-foreground" for="s-when-{i}">When (CEL)</label>
						<Input
							id="s-when-{i}"
							value={r.when}
							disabled={readonly}
							oninput={(e) => edit(i, { when: e.currentTarget.value })}
						/>
					</div>
					<div class="space-y-1">
						<label class="text-xs text-muted-foreground" for="s-retry-{i}">Retry attempts</label>
						<Input
							id="s-retry-{i}"
							class="w-24"
							inputmode="numeric"
							value={r.max_attempts}
							disabled={readonly}
							oninput={(e) => edit(i, { max_attempts: e.currentTarget.value })}
						/>
					</div>
					<label class="flex items-center gap-1 pb-2 text-sm">
						<input
							type="checkbox"
							checked={r.continue_on_error}
							disabled={readonly}
							onchange={(e) => edit(i, { continue_on_error: e.currentTarget.checked })}
						/>
						continue on error
					</label>
				</div>
				<div class="space-y-1">
					<label class="text-xs text-muted-foreground" for="s-with-{i}">Input (with, JSON)</label>
					<textarea
						id="s-with-{i}"
						class="h-24 w-full rounded-md border bg-background p-2 font-mono text-xs"
						spellcheck="false"
						{readonly}
						value={pretty(r.with)}
						onchange={(e) => json(i, 'with', e.currentTarget.value)}
						data-testid="step-with-{i}"></textarea>
					{#if bad[`with-${i}`]}
						<p class="text-xs text-destructive" role="alert">{bad[`with-${i}`]}</p>
					{/if}
				</div>
			{/if}
			{#if r.complex}
				<details class="space-y-1" open={r.raw.type === 'group'}>
					<summary class="cursor-pointer text-xs text-muted-foreground"
						>{r.raw.type === 'group'
							? 'Group: whole step as JSON'
							: 'More fields on this step: whole step as JSON'}</summary
					>
					<textarea
						class="h-40 w-full rounded-md border bg-background p-2 font-mono text-xs"
						spellcheck="false"
						{readonly}
						aria-label="Step {i + 1} as JSON"
						value={pretty(r.raw)}
						onchange={(e) => json(i, 'whole', e.currentTarget.value)}
						data-testid="step-whole-{i}"></textarea>
					{#if bad[`whole-${i}`]}
						<p class="text-xs text-destructive" role="alert">{bad[`whole-${i}`]}</p>
					{/if}
				</details>
			{/if}
		</section>
	{/each}
	{#if !readonly}
		<Button variant="outline" onclick={() => onchange(addStep(spec))} data-testid="step-add"
			>Add step</Button
		>
	{/if}
</div>
