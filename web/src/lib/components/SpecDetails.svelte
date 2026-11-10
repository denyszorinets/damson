<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { readDetails, setDetails, type Spec } from '$lib/specmodel';

	/** The Details tab: name, description, whole-run timeout and node selector. */
	let {
		spec,
		readonly,
		onchange
	}: { spec: Spec; readonly: boolean; onchange: (next: Spec) => void } = $props();

	const d = $derived(readDetails(spec));
	const edit = (patch: Partial<ReturnType<typeof readDetails>>) =>
		onchange(setDetails(spec, { ...d, ...patch }));
</script>

<div class="max-w-xl space-y-4" data-testid="spec-details">
	<div class="space-y-1">
		<label class="text-sm font-medium" for="d-name">Name</label>
		<Input
			id="d-name"
			value={d.name}
			disabled={readonly}
			oninput={(e) => edit({ name: e.currentTarget.value })}
			data-testid="detail-name"
		/>
	</div>
	<div class="space-y-1">
		<label class="text-sm font-medium" for="d-desc">Description</label>
		<Input
			id="d-desc"
			value={d.description}
			disabled={readonly}
			oninput={(e) => edit({ description: e.currentTarget.value })}
			data-testid="detail-description"
		/>
	</div>
	<div class="space-y-1">
		<label class="text-sm font-medium" for="d-timeout">Timeout for the whole run</label>
		<Input
			id="d-timeout"
			value={d.timeout}
			placeholder="30m"
			disabled={readonly}
			oninput={(e) => edit({ timeout: e.currentTarget.value })}
			data-testid="detail-timeout"
		/>
	</div>
	<div class="space-y-1">
		<label class="text-sm font-medium" for="d-selector">Node selector (CEL)</label>
		<Input
			id="d-selector"
			value={d.selector}
			disabled={readonly}
			oninput={(e) => edit({ selector: e.currentTarget.value })}
			data-testid="detail-selector"
		/>
	</div>
</div>
