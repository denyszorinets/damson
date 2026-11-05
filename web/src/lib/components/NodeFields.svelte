<script lang="ts">
	import * as Select from '$lib/components/ui/select';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Textarea } from '$lib/components/ui/textarea';
	import type { NodeForm } from '$lib/nodes';

	let { form = $bindable(), editing = false }: { form: NodeForm; editing?: boolean } = $props();
</script>

<div class="grid max-w-xl gap-3">
	<div class="grid gap-1">
		<Label for="n-name">Name</Label>
		<Input id="n-name" bind:value={form.name} disabled={editing} required />
	</div>
	<div class="grid gap-1">
		<Label for="n-address">Address</Label>
		<Input
			id="n-address"
			bind:value={form.address}
			placeholder="host.example.com or 10.0.0.7"
			required
		/>
	</div>
	<div class="grid grid-cols-2 gap-3">
		<div class="grid gap-1">
			<Label for="n-port">Port</Label>
			<Input id="n-port" bind:value={form.port} placeholder="22 / 5986" />
		</div>
		<div class="grid gap-1">
			<Label for="n-transport">Transport</Label>
			<Select.Root type="single" bind:value={form.transport}>
				<Select.Trigger id="n-transport" data-testid="node-transport"
					>{form.transport}</Select.Trigger
				>
				<Select.Content>
					<Select.Item value="ssh">ssh</Select.Item>
					<Select.Item value="winrm">winrm</Select.Item>
				</Select.Content>
			</Select.Root>
		</div>
	</div>
	<div class="grid grid-cols-2 gap-3">
		<div class="grid gap-1">
			<Label for="n-os">OS</Label>
			<Input id="n-os" bind:value={form.os} placeholder="linux" />
		</div>
		<div class="grid gap-1">
			<Label for="n-pool">Pool</Label>
			<Input id="n-pool" bind:value={form.pool} />
		</div>
	</div>
	<div class="grid gap-1">
		<Label for="n-tags">Tags (comma separated)</Label>
		<Input id="n-tags" bind:value={form.tags} />
	</div>
	<div class="grid gap-1">
		<Label for="n-attrs">Attributes (JSON object)</Label>
		<Textarea id="n-attrs" bind:value={form.attributes} rows={3} placeholder={'{"env": "prod"}'} />
	</div>
	<div class="grid gap-1">
		<Label for="n-cred">Credential (a secret:// reference)</Label>
		<Input id="n-cred" bind:value={form.credential} placeholder="secret://_inventory/web-key" />
	</div>
	<div class="grid gap-1">
		<Label for="n-hostkey">Host key (authorized_keys form)</Label>
		<Textarea id="n-hostkey" bind:value={form.host_key} rows={2} />
	</div>
	<div class="grid grid-cols-2 gap-3">
		<div class="grid gap-1">
			<Label for="n-policy">Host key policy</Label>
			<Select.Root type="single" bind:value={form.host_key_policy}>
				<Select.Trigger id="n-policy" data-testid="node-policy"
					>{form.host_key_policy}</Select.Trigger
				>
				<Select.Content>
					<Select.Item value="strict">strict</Select.Item>
					<Select.Item value="tofu">tofu</Select.Item>
					<Select.Item value="insecure">insecure</Select.Item>
				</Select.Content>
			</Select.Root>
		</div>
		<label class="flex items-end gap-2 pb-2 text-sm">
			<input type="checkbox" bind:checked={form.enabled} /> Enabled
		</label>
	</div>
</div>
