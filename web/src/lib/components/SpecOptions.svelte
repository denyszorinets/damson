<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import {
		OPTION_TYPES,
		optionRows,
		setOptionRows,
		type OptionRow,
		type OptionType,
		type Spec
	} from '$lib/specmodel';

	/** The Options tab: the run inputs, one row per property of the options schema. */
	let {
		spec,
		readonly,
		onchange
	}: { spec: Spec; readonly: boolean; onchange: (next: Spec) => void } = $props();

	const rows = $derived(optionRows(spec));
	const write = (next: OptionRow[]) => onchange(setOptionRows(spec, next));
	// A name that is empty or already taken would merge two options into one: refused.
	let nameError = $state<string | null>(null);
	function edit(i: number, patch: Partial<OptionRow>) {
		if (patch.name !== undefined) {
			const taken = rows.some((r, n) => n !== i && r.name === patch.name);
			nameError =
				patch.name === ''
					? 'An option needs a name.'
					: taken
						? `"${patch.name}" is already used.`
						: null;
			if (nameError) return;
		}
		write(rows.map((r, n) => (n === i ? { ...r, ...patch } : r)));
	}
	function add() {
		let n = rows.length + 1;
		while (rows.some((r) => r.name === `option${n}`)) n++;
		write([
			...rows,
			{
				name: `option${n}`,
				type: 'string',
				title: '',
				default: '',
				required: false,
				advanced: false
			}
		]);
	}
</script>

<div class="space-y-3" data-testid="spec-options">
	{#if rows.length === 0}
		<p class="text-sm text-muted-foreground">This job takes no options.</p>
	{/if}
	{#if nameError}
		<p class="text-sm text-destructive" role="alert" data-testid="option-name-error">{nameError}</p>
	{/if}
	{#each rows as r, i (i)}
		<div class="flex flex-wrap items-end gap-2 rounded-md border p-3" data-testid="option-row">
			<div class="space-y-1">
				<label class="text-xs text-muted-foreground" for="o-name-{i}">Name</label>
				<Input
					id="o-name-{i}"
					class="w-36"
					value={r.name}
					disabled={readonly}
					oninput={(e) => edit(i, { name: e.currentTarget.value })}
					data-testid="option-name-{i}"
				/>
			</div>
			{#if r.advanced}
				<p class="text-xs text-muted-foreground">
					Uses schema keywords this tab does not show; edit it in the JSON tab.
				</p>
			{:else}
				<div class="space-y-1">
					<label class="text-xs text-muted-foreground" for="o-type-{i}">Type</label>
					<select
						id="o-type-{i}"
						class="h-9 rounded-md border bg-background px-2 text-sm"
						value={r.type}
						disabled={readonly}
						onchange={(e) => edit(i, { type: e.currentTarget.value as OptionType })}
					>
						{#each OPTION_TYPES as t (t)}<option value={t}>{t}</option>{/each}
					</select>
				</div>
				<div class="space-y-1">
					<label class="text-xs text-muted-foreground" for="o-title-{i}">Title</label>
					<Input
						id="o-title-{i}"
						class="w-40"
						value={r.title}
						disabled={readonly}
						oninput={(e) => edit(i, { title: e.currentTarget.value })}
					/>
				</div>
				<div class="space-y-1">
					<label class="text-xs text-muted-foreground" for="o-default-{i}">Default</label>
					<Input
						id="o-default-{i}"
						class="w-32"
						value={r.default}
						disabled={readonly}
						oninput={(e) => edit(i, { default: e.currentTarget.value })}
					/>
				</div>
			{/if}
			<label class="flex items-center gap-1 pb-2 text-sm">
				<input
					type="checkbox"
					checked={r.required}
					disabled={readonly}
					onchange={(e) => edit(i, { required: e.currentTarget.checked })}
				/>
				required
			</label>
			{#if !readonly}
				<Button variant="outline" size="sm" onclick={() => write(rows.filter((_, n) => n !== i))}
					>Remove</Button
				>
			{/if}
		</div>
	{/each}
	{#if !readonly}
		<Button variant="outline" onclick={add} data-testid="option-add">Add option</Button>
	{/if}
</div>
