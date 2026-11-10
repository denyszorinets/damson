<script lang="ts">
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	/** Asks for the admin token; the parent keeps it (sessionStorage) and decides when to forget it. */
	let { onsubmit }: { onsubmit: (token: string) => void } = $props();
	let value = $state('');
</script>

<form
	class="max-w-md space-y-3"
	data-testid="token-form"
	onsubmit={(e) => {
		e.preventDefault();
		const t = value.trim();
		value = '';
		if (t) onsubmit(t);
	}}
>
	<Label for="admin-token">Admin token</Label>
	<Input id="admin-token" type="password" autocomplete="off" placeholder="dma_..." bind:value />
	<p class="text-sm text-muted-foreground">
		Create one with <code>damson admin admin-token create</code>. It is kept in this tab only.
	</p>
	<Button type="submit">Use token</Button>
</form>
