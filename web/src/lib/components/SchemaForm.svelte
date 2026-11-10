<script lang="ts">
	// The one place a JSON Schema becomes a form (CLAUDE.md, frontend rules). It
	// wraps @sjsf/form with its basic theme and the Ajv validator. Plugins never
	// get UI code of their own: their forms come from their schemas through here.
	import { createForm, BasicForm, type Schema } from '@sjsf/form';
	import { resolver } from '@sjsf/form/resolvers/compat';
	import { translation } from '@sjsf/form/translations/en';
	import { createFormMerger } from '@sjsf/form/mergers/modern';
	import { createFormIdBuilder } from '@sjsf/form/id-builders/modern';
	import { createFormValidator } from '@sjsf/ajv8-validator';
	import { theme } from '@sjsf/basic-theme';
	import '@sjsf/form/fields/extra/enum-include'; // enum properties render as a select
	import '@sjsf/basic-theme/css/basic.css';

	let {
		schema,
		initial = {},
		onsubmit
	}: {
		schema: Record<string, unknown>;
		initial?: Record<string, unknown>;
		onsubmit: (values: Record<string, unknown>) => void;
	} = $props();

	// The schema and initial values are read once: a different schema means a
	// different form, which the parent makes by re-creating this component.
	// svelte-ignore state_referenced_locally
	const form = createForm({
		theme,
		schema: schema as Schema,
		initialValue: initial,
		resolver,
		translation,
		merger: createFormMerger,
		validator: createFormValidator,
		idBuilder: createFormIdBuilder,
		onSubmit: (value: unknown) => onsubmit((value ?? {}) as Record<string, unknown>)
	});
</script>

<div class="schema-form max-w-xl" data-testid="schema-form">
	<BasicForm {form} novalidate />
</div>
